import * as path from "node:path";
import { CfnOutput, Duration, RemovalPolicy, Stack, StackProps } from "aws-cdk-lib";
import * as cloudfront from "aws-cdk-lib/aws-cloudfront";
import * as origins from "aws-cdk-lib/aws-cloudfront-origins";
import * as ec2 from "aws-cdk-lib/aws-ec2";
import { DockerImageAsset, Platform } from "aws-cdk-lib/aws-ecr-assets";
import * as ecs from "aws-cdk-lib/aws-ecs";
import * as elasticache from "aws-cdk-lib/aws-elasticache";
import * as elbv2 from "aws-cdk-lib/aws-elasticloadbalancingv2";
import * as logs from "aws-cdk-lib/aws-logs";
import { Construct } from "constructs";

import { Config, Mode } from "./config";
import { DataStack } from "./data-stack";

export interface AppStackProps extends StackProps {
  readonly data: DataStack;
  readonly config: Config;
  readonly mode: Mode;
}

const repoRoot = path.join(__dirname, "..", "..");

const SERVER_PORT = 8080;
const WEB_PORT = 3000;
const CACHE_PORT = 6379;

/**
 * Everything that costs money while it runs: the network, the cache, the
 * three services, the load balancer, and CloudFront. Destroy it between
 * demos; the data stack is untouched by that.
 */
export class AppStack extends Stack {
  constructor(scope: Construct, id: string, props: AppStackProps) {
    super(scope, id, { ...props, description: "Monster Theatre - network, cache, services, load balancer, CloudFront" });
    const { data, config, mode } = props;

    // No NAT gateway. The tasks sit in the public subnets with a public
    // address each, because they must reach Temporal Cloud, the providers,
    // and the DSQL endpoint; their security groups let nothing in from the
    // internet. The cache and the load balancer are in subnets with no
    // route out at all.
    const vpc = new ec2.Vpc(this, "Vpc", {
      maxAzs: 2,
      natGateways: 0,
      subnetConfiguration: [
        { name: "public", subnetType: ec2.SubnetType.PUBLIC, cidrMask: 24 },
        { name: "isolated", subnetType: ec2.SubnetType.PRIVATE_ISOLATED, cidrMask: 24 },
      ],
    });

    const group = (id: string, description: string) =>
      new ec2.SecurityGroup(this, id, { vpc, description, allowAllOutbound: true });
    const loadBalancerGroup = group("LoadBalancerGroup", "Monster Theatre load balancer");
    const serverGroup = group("ServerGroup", "Monster Theatre server tasks");
    const workerGroup = group("WorkerGroup", "Monster Theatre worker tasks");
    const webGroup = group("WebGroup", "Monster Theatre web tasks");
    const cacheGroup = group("CacheGroup", "Monster Theatre cache");

    // The load balancer has no public address; only a CloudFront VPC origin
    // can send it anything. CloudFront's requests keep CloudFront's own
    // source addresses, though, so the rule names CloudFront's managed list
    // of them. A rule for this VPC's range would let nothing in.
    const cloudFront = ec2.PrefixList.fromLookup(this, "CloudFrontOriginFacing", {
      prefixListName: "com.amazonaws.global.cloudfront.origin-facing",
    });
    loadBalancerGroup.addIngressRule(ec2.Peer.prefixList(cloudFront.prefixListId), ec2.Port.tcp(80), "CloudFront origin-facing addresses");

    // --- Cache: the two Redis streams (D024, D046) ---
    for (const [name, from] of [["server", serverGroup], ["worker", workerGroup]] as const) {
      // A rule of its own, not one inline in the group, so each is visible
      // as "from this group" in the template.
      new ec2.CfnSecurityGroupIngress(this, `CacheFrom${name}`, {
        groupId: cacheGroup.securityGroupId,
        sourceSecurityGroupId: from.securityGroupId,
        ipProtocol: "tcp",
        fromPort: CACHE_PORT,
        toPort: CACHE_PORT,
        description: `Monster Theatre ${name} tasks`,
      });
    }
    const cacheSubnets = new elasticache.CfnSubnetGroup(this, "CacheSubnets", {
      description: "Monster Theatre cache",
      subnetIds: vpc.isolatedSubnets.map((s) => s.subnetId),
    });
    // One node, cluster mode off: the server reads both streams in a single
    // XREAD, which a clustered cache can refuse. Valkey 8.1 is what the
    // local container runs.
    const cache = new elasticache.CfnReplicationGroup(this, "Cache", {
      replicationGroupDescription: "Monster Theatre streams",
      engine: "valkey",
      engineVersion: "8.1",
      cacheNodeType: "cache.t4g.micro",
      numCacheClusters: 1,
      automaticFailoverEnabled: false,
      transitEncryptionEnabled: true,
      atRestEncryptionEnabled: true,
      cacheSubnetGroupName: cacheSubnets.ref,
      securityGroupIds: [cacheGroup.securityGroupId],
    });
    const redisUrl = `rediss://${cache.attrPrimaryEndPointAddress}:${cache.attrPrimaryEndPointPort}/0`;

    // --- Load balancer and CloudFront ---
    const loadBalancer = new elbv2.ApplicationLoadBalancer(this, "LoadBalancer", {
      vpc,
      internetFacing: false,
      vpcSubnets: { subnetType: ec2.SubnetType.PRIVATE_ISOLATED },
      securityGroup: loadBalancerGroup,
      // The server pings every browser each 25 s, so a WebSocket is never
      // idle this long.
      idleTimeout: Duration.seconds(60),
    });

    // There is no domain name, so no certificate of our own: CloudFront's
    // address and certificate are the public ones. The hop from CloudFront
    // to the load balancer is plain HTTP inside AWS's network.
    const origin = origins.VpcOrigin.withApplicationLoadBalancer(loadBalancer, {
      protocolPolicy: cloudfront.OriginProtocolPolicy.HTTP_ONLY,
    });
    const distribution = new cloudfront.Distribution(this, "Distribution", {
      comment: "Monster Theatre",
      defaultBehavior: {
        origin,
        viewerProtocolPolicy: cloudfront.ViewerProtocolPolicy.REDIRECT_TO_HTTPS,
        allowedMethods: cloudfront.AllowedMethods.ALLOW_ALL,
        // Nothing is cached, and every header is passed on: cookies, Origin,
        // and the WebSocket upgrade. Host too, so the server sees the
        // address the browser used.
        cachePolicy: cloudfront.CachePolicy.CACHING_DISABLED,
        originRequestPolicy: cloudfront.OriginRequestPolicy.ALL_VIEWER,
      },
      additionalBehaviors: {
        // Next.js names these files by their content, so they never change.
        "/_next/static/*": {
          origin,
          viewerProtocolPolicy: cloudfront.ViewerProtocolPolicy.REDIRECT_TO_HTTPS,
          cachePolicy: cloudfront.CachePolicy.CACHING_OPTIMIZED,
        },
      },
    });
    const publicBaseUrl = `https://${distribution.distributionDomainName}`;

    // --- Services ---
    const cluster = new ecs.Cluster(this, "Cluster", { vpc });

    const serverImage = new DockerImageAsset(this, "ServerImage", {
      directory: path.join(repoRoot, "server"),
      platform: Platform.LINUX_ARM64,
    });
    const webImage = new DockerImageAsset(this, "WebImage", {
      directory: path.join(repoRoot, "web"),
      platform: Platform.LINUX_ARM64,
    });

    const database = {
      DSQL_ENDPOINT: data.clusterEndpoint,
      // The scoped role `migrate grant` creates; server/internal/db.AppRole.
      DSQL_USER: "monster_app",
    };
    const temporal = {
      TEMPORAL_ADDRESS: config.temporalAddress,
      TEMPORAL_NAMESPACE: config.temporalNamespace,
      TEMPORAL_TASK_QUEUE: "performance",
    };
    const temporalKey = ecs.Secret.fromSecretsManager(data.temporalApiKey);

    const server = this.service("Server", {
      cluster,
      securityGroup: serverGroup,
      taskRole: data.serverRole,
      container: {
        containerName: "server",
        image: ecs.ContainerImage.fromDockerImageAsset(serverImage),
        portMappings: [{ containerPort: SERVER_PORT }],
        environment: {
          HTTP_ADDR: `:${SERVER_PORT}`,
          ...database,
          ...temporal,
          S3_BUCKET: data.bucket.bucketName,
          REDIS_URL: redisUrl,
          // Exactly what the browser's address bar shows, or every
          // WebSocket is refused (D035). COOKIE_SECURE is left on.
          PUBLIC_BASE_URL: publicBaseUrl,
          VOICE_ID_A: config.voiceIdA,
          VOICE_ID_B: config.voiceIdB,
        },
        secrets: {
          SHARE_LINK_SECRET: ecs.Secret.fromSecretsManager(data.shareLinkSecret),
          TEMPORAL_API_KEY: temporalKey,
        },
      },
    });

    // In test mode the worker is not even given the provider keys, so that
    // deployment cannot spend anything on Claude, ElevenLabs, or WaveSpeed.
    const providers: { environment: Record<string, string>; secrets: Record<string, ecs.Secret> } =
      mode === "live"
        ? {
            environment: {
              LLM_PROVIDER: "anthropic",
              TTS_PROVIDER: "elevenlabs",
              LLM_MODEL: config.llmModel,
              TTS_MODEL: config.ttsModel,
              // Used only for a performance whose director ticked
              // "Include lip-sync video" (D055).
              LIPSYNC_PROVIDER: "wavespeed",
              LIPSYNC_RESOLUTION: config.lipSyncResolution ?? "480p",
            },
            secrets: {
              ANTHROPIC_API_KEY: ecs.Secret.fromSecretsManager(data.anthropicApiKey),
              ELEVENLABS_API_KEY: ecs.Secret.fromSecretsManager(data.elevenLabsApiKey),
              WAVESPEED_API_KEY: ecs.Secret.fromSecretsManager(data.waveSpeedApiKey),
            },
          }
        : {
            environment: {
              LLM_PROVIDER: "fake",
              TTS_PROVIDER: "fake",
              LIPSYNC_PROVIDER: "fake",
              // The timings the end-to-end suite expects (tests/e2e/harness).
              VOTE_WINDOW: "2s",
              PREMIERE_COUNTDOWN: "1s",
              CHOICES_RETRY_WINDOW: "4s",
            },
            secrets: {},
          };
    this.service("Worker", {
      cluster,
      securityGroup: workerGroup,
      taskRole: data.workerRole,
      container: {
        containerName: "worker",
        image: ecs.ContainerImage.fromDockerImageAsset(serverImage),
        entryPoint: ["/usr/local/bin/worker"],
        environment: {
          ...database,
          ...temporal,
          S3_BUCKET: data.bucket.bucketName,
          REDIS_URL: redisUrl,
          ...providers.environment,
        },
        secrets: { TEMPORAL_API_KEY: temporalKey, ...providers.secrets },
      },
    });

    const web = this.service("Web", {
      cluster,
      securityGroup: webGroup,
      container: {
        containerName: "web",
        image: ecs.ContainerImage.fromDockerImageAsset(webImage),
        portMappings: [{ containerPort: WEB_PORT }],
      },
    });

    // --- Routing: /api/* (the WebSocket included) to the server, the rest
    // to the web app. No stickiness: any server can serve any browser (D046).
    const listener = loadBalancer.addListener("Http", { port: 80, protocol: elbv2.ApplicationProtocol.HTTP, open: false });
    listener.addTargets("Web", {
      port: WEB_PORT,
      protocol: elbv2.ApplicationProtocol.HTTP,
      targets: [web.loadBalancerTarget({ containerName: "web", containerPort: WEB_PORT })],
      healthCheck: { path: "/login", healthyHttpCodes: "200" },
      deregistrationDelay: Duration.seconds(10),
    });
    listener.addTargets("Server", {
      priority: 10,
      conditions: [elbv2.ListenerCondition.pathPatterns(["/api/*"])],
      port: SERVER_PORT,
      protocol: elbv2.ApplicationProtocol.HTTP,
      targets: [server.loadBalancerTarget({ containerName: "server", containerPort: SERVER_PORT })],
      // /livez checks nothing but the process. /healthz also asks the
      // database, the cache, and Temporal, and one of those being briefly
      // unreachable must not close every WebSocket.
      healthCheck: { path: "/livez", healthyHttpCodes: "200" },
      deregistrationDelay: Duration.seconds(30),
    });
    serverGroup.addIngressRule(loadBalancerGroup, ec2.Port.tcp(SERVER_PORT), "Monster Theatre load balancer");
    webGroup.addIngressRule(loadBalancerGroup, ec2.Port.tcp(WEB_PORT), "Monster Theatre load balancer");

    new CfnOutput(this, "Url", { value: publicBaseUrl, description: "Open this. It is also PUBLIC_BASE_URL" });
    new CfnOutput(this, "Mode", { value: mode });
    new CfnOutput(this, "ClusterName", { value: cluster.clusterName });
  }

  /** One Fargate service of one small arm64 task, logging to its own group. */
  private service(
    id: string,
    props: {
      cluster: ecs.Cluster;
      securityGroup: ec2.SecurityGroup;
      taskRole?: ecs.FargateTaskDefinitionProps["taskRole"];
      container: Omit<ecs.ContainerDefinitionOptions, "logging">;
    },
  ): ecs.FargateService {
    const taskDefinition = new ecs.FargateTaskDefinition(this, `${id}Task`, {
      cpu: 256,
      memoryLimitMiB: 512,
      runtimePlatform: {
        cpuArchitecture: ecs.CpuArchitecture.ARM64,
        operatingSystemFamily: ecs.OperatingSystemFamily.LINUX,
      },
      taskRole: props.taskRole,
    });
    taskDefinition.addContainer(id, {
      ...props.container,
      logging: ecs.LogDrivers.awsLogs({
        streamPrefix: id.toLowerCase(),
        logGroup: new logs.LogGroup(this, `${id}Logs`, {
          retention: logs.RetentionDays.ONE_WEEK,
          removalPolicy: RemovalPolicy.DESTROY,
        }),
      }),
    });
    return new ecs.FargateService(this, `${id}Service`, {
      cluster: props.cluster,
      taskDefinition,
      desiredCount: 1,
      assignPublicIp: true,
      vpcSubnets: { subnetType: ec2.SubnetType.PUBLIC },
      securityGroups: [props.securityGroup],
      // A deployment that cannot become healthy is rolled back, not retried
      // for ever.
      circuitBreaker: { rollback: true },
      minHealthyPercent: 100,
      maxHealthyPercent: 200,
    });
  }
}
