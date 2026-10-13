import { test } from "node:test";
import assert from "node:assert/strict";
import { App } from "aws-cdk-lib";
import { Match, Template } from "aws-cdk-lib/assertions";

import { Config, Mode } from "../lib/config";
import { DataStack } from "../lib/data-stack";
import { AppStack } from "../lib/app-stack";

const config: Config = {
  region: "ap-southeast-1",
  temporalAddress: "theatre.a1b2c.tmprl.cloud:7233",
  temporalNamespace: "theatre.a1b2c",
  voiceIdA: "voice-a",
  voiceIdB: "voice-b",
  llmModel: "claude-sonnet-5-5",
  ttsModel: "eleven_v4_turbo",
};

function synth(mode: Mode): { data: Template; app: Template } {
  const cdkApp = new App();
  const env = { account: "123456789012", region: config.region };
  const data = new DataStack(cdkApp, "MonsterTheatreData", { env });
  const app = new AppStack(cdkApp, "MonsterTheatreApp", { env, data, config, mode });
  return { data: Template.fromStack(data), app: Template.fromStack(app) };
}

type Container = {
  Name: string;
  Environment?: { Name: string; Value: unknown }[];
  Secrets?: { Name: string; ValueFrom: unknown }[];
};

// The one container of the task definition whose container has this name.
function container(template: Template, name: string): Container {
  const definitions = Object.values(template.findResources("AWS::ECS::TaskDefinition"));
  const containers = definitions.flatMap((d) => d.Properties.ContainerDefinitions as Container[]);
  const found = containers.filter((c) => c.Name === name);
  assert.equal(found.length, 1, `one container named ${name}`);
  return found[0];
}

function env(c: Container): Record<string, unknown> {
  return Object.fromEntries((c.Environment ?? []).map((e) => [e.Name, e.Value]));
}

function secretNames(c: Container): string[] {
  return (c.Secrets ?? []).map((s) => s.Name).sort();
}

test("the bucket is private, encrypted, TLS-only, and expires objects after seven days", () => {
  const { data } = synth("test");
  data.resourceCountIs("AWS::S3::Bucket", 1);
  data.hasResourceProperties("AWS::S3::Bucket", {
    PublicAccessBlockConfiguration: {
      BlockPublicAcls: true,
      BlockPublicPolicy: true,
      IgnorePublicAcls: true,
      RestrictPublicBuckets: true,
    },
    BucketEncryption: {
      ServerSideEncryptionConfiguration: [{ ServerSideEncryptionByDefault: { SSEAlgorithm: "AES256" } }],
    },
    LifecycleConfiguration: { Rules: [Match.objectLike({ ExpirationInDays: 7, Status: "Enabled" })] },
  });
  data.hasResourceProperties("AWS::S3::BucketPolicy", {
    PolicyDocument: {
      Statement: Match.arrayWith([
        Match.objectLike({ Effect: "Deny", Condition: { Bool: { "aws:SecureTransport": "false" } } }),
      ]),
    },
  });
});

test("the database is one Aurora DSQL cluster that cannot be deleted by accident", () => {
  const { data } = synth("test");
  data.resourceCountIs("AWS::DSQL::Cluster", 1);
  data.hasResourceProperties("AWS::DSQL::Cluster", { DeletionProtectionEnabled: true });
});

test("there are four secrets, and only the share-link one is given a generated value on purpose", () => {
  const { data } = synth("test");
  data.resourceCountIs("AWS::SecretsManager::Secret", 4);
  for (const name of ["anthropic-api-key", "elevenlabs-api-key", "temporal-api-key", "share-link-secret"]) {
    data.hasResourceProperties("AWS::SecretsManager::Secret", { Name: `monster-theatre/${name}` });
  }
  data.hasResourceProperties("AWS::SecretsManager::Secret", {
    Name: "monster-theatre/share-link-secret",
    GenerateSecretString: Match.objectLike({ PasswordLength: 64, ExcludePunctuation: true }),
  });
});

test("the task roles may connect to the cluster as an application, not as admin", () => {
  const { data } = synth("test");
  const policies = JSON.stringify(data.findResources("AWS::IAM::Policy"));
  assert.match(policies, /dsql:DbConnect"/);
  assert.doesNotMatch(policies, /dsql:DbConnectAdmin/);
  assert.doesNotMatch(policies, /"dsql:\*"/);
});

test("there is no NAT gateway, and the tasks have public addresses instead", () => {
  const { app } = synth("test");
  app.resourceCountIs("AWS::EC2::NatGateway", 0);
  app.resourceCountIs("AWS::ECS::Service", 3);
  app.allResourcesProperties("AWS::ECS::Service", {
    DesiredCount: 1,
    LaunchType: "FARGATE",
    NetworkConfiguration: { AwsvpcConfiguration: Match.objectLike({ AssignPublicIp: "ENABLED" }) },
  });
});

test("every task is arm64 with a quarter CPU and half a gigabyte", () => {
  const { app } = synth("test");
  app.resourceCountIs("AWS::ECS::TaskDefinition", 3);
  app.allResourcesProperties("AWS::ECS::TaskDefinition", {
    Cpu: "256",
    Memory: "512",
    RuntimePlatform: { CpuArchitecture: "ARM64", OperatingSystemFamily: "LINUX" },
  });
});

test("the load balancer is internal, sends /api/* to the server and the rest to the web app", () => {
  const { app } = synth("test");
  app.resourceCountIs("AWS::ElasticLoadBalancingV2::LoadBalancer", 1);
  app.hasResourceProperties("AWS::ElasticLoadBalancingV2::LoadBalancer", { Scheme: "internal", Type: "application" });

  // The target group each port belongs to.
  const groups = app.findResources("AWS::ElasticLoadBalancingV2::TargetGroup");
  const groupOnPort = (port: number) => {
    const ids = Object.keys(groups).filter((id) => groups[id].Properties.Port === port);
    assert.equal(ids.length, 1, `one target group on port ${port}`);
    return ids[0];
  };
  const server = groupOnPort(8080);
  const web = groupOnPort(3000);
  assert.equal(groups[server].Properties.HealthCheckPath, "/livez");
  assert.equal(groups[web].Properties.HealthCheckPath, "/login");

  app.hasResourceProperties("AWS::ElasticLoadBalancingV2::Listener", {
    Port: 80,
    Protocol: "HTTP",
    DefaultActions: [Match.objectLike({ Type: "forward", TargetGroupArn: { Ref: web } })],
  });
  app.resourceCountIs("AWS::ElasticLoadBalancingV2::ListenerRule", 1);
  app.hasResourceProperties("AWS::ElasticLoadBalancingV2::ListenerRule", {
    Conditions: [{ Field: "path-pattern", PathPatternConfig: { Values: ["/api/*"] } }],
    Actions: [Match.objectLike({ Type: "forward", TargetGroupArn: { Ref: server } })],
  });
  // No stickiness anywhere: a room's people may be on different servers (D046).
  assert.doesNotMatch(JSON.stringify(groups), /stickiness\.enabled","Value":"true"/);
});

// CloudFront's requests do not arrive from addresses inside the VPC, so a
// rule for the VPC's own range lets nothing in (the first deployment
// answered 504 for that reason). The documented ways in are CloudFront's
// managed prefix list or its service security group.
test("the load balancer accepts port 80 from CloudFront's origin-facing prefix list and nothing else", () => {
  const { app } = synth("test");
  const groups = app.findResources("AWS::EC2::SecurityGroup");
  const [id] = Object.keys(groups).filter((g) => groups[g].Properties.GroupDescription === "Monster Theatre load balancer");
  assert.ok(id, "the load balancer's security group");

  // A rule may be written inside the group or as a resource of its own.
  const inline = (groups[id].Properties.SecurityGroupIngress ?? []) as Record<string, unknown>[];
  const separate = Object.values(app.findResources("AWS::EC2::SecurityGroupIngress"))
    .map((r) => r.Properties as Record<string, unknown>)
    .filter((r) => JSON.stringify(r.GroupId).includes(id));
  const ingress = [...inline, ...separate];

  assert.equal(ingress.length, 1);
  assert.equal(ingress[0].FromPort, 80);
  assert.equal(ingress[0].ToPort, 80);
  assert.ok(ingress[0].SourcePrefixListId, "from a prefix list");
  assert.equal(ingress[0].CidrIp, undefined, "not from an address range");
});

test("CloudFront serves HTTPS only and reaches the load balancer privately", () => {
  const { app } = synth("test");
  app.resourceCountIs("AWS::CloudFront::Distribution", 1);
  app.resourceCountIs("AWS::CloudFront::VpcOrigin", 1);
  app.hasResourceProperties("AWS::CloudFront::Distribution", {
    DistributionConfig: Match.objectLike({
      Enabled: true,
      DefaultCacheBehavior: Match.objectLike({
        ViewerProtocolPolicy: "redirect-to-https",
        AllowedMethods: ["GET", "HEAD", "OPTIONS", "PUT", "PATCH", "POST", "DELETE"],
        // The managed CachingDisabled policy: API answers and the WebSocket are never cached.
        CachePolicyId: "4135ea2d-6df8-44a3-9df3-4b5a84be39ad",
      }),
    }),
  });
});

test("the server is told its public address is the CloudFront one, and its cookies stay Secure", () => {
  const { app } = synth("test");
  const server = container(app, "server");
  const vars = env(server);
  const [distribution] = Object.keys(app.findResources("AWS::CloudFront::Distribution"));
  assert.deepEqual(
    vars.PUBLIC_BASE_URL,
    { "Fn::Join": ["", ["https://", { "Fn::GetAtt": [distribution, "DomainName"] }]] },
    "PUBLIC_BASE_URL is https:// and the distribution's own domain name",
  );
  assert.equal(vars.COOKIE_SECURE, undefined, "COOKIE_SECURE is left at its default, on");
  assert.equal(vars.DSQL_USER, "monster_app");
  assert.equal(vars.TEMPORAL_ADDRESS, config.temporalAddress);
  assert.equal(vars.TEMPORAL_NAMESPACE, config.temporalNamespace);
  assert.equal(vars.VOICE_ID_A, "voice-a");
  assert.equal(vars.VOICE_ID_B, "voice-b");
  assert.match(JSON.stringify(vars.REDIS_URL), /rediss:\/\//, "Valkey is reached over TLS");
  assert.deepEqual(secretNames(server), ["SHARE_LINK_SECRET", "TEMPORAL_API_KEY"]);
});

test("no secret value is written into a task definition", () => {
  for (const mode of ["test", "live"] as const) {
    const { app } = synth(mode);
    for (const name of ["server", "worker", "web"]) {
      const names = Object.keys(env(container(app, name)));
      for (const secret of ["SHARE_LINK_SECRET", "TEMPORAL_API_KEY", "ANTHROPIC_API_KEY", "ELEVENLABS_API_KEY"]) {
        assert.ok(!names.includes(secret), `${mode}/${name}: ${secret} is not a plain variable`);
      }
    }
  }
});

test("test mode uses the stand-in providers and is not given the provider keys", () => {
  const { app } = synth("test");
  const worker = container(app, "worker");
  const vars = env(worker);
  assert.equal(vars.LLM_PROVIDER, "fake");
  assert.equal(vars.TTS_PROVIDER, "fake");
  assert.equal(vars.VOTE_WINDOW, "2s");
  assert.equal(vars.PREMIERE_COUNTDOWN, "1s");
  assert.equal(vars.CHOICES_RETRY_WINDOW, "4s");
  assert.deepEqual(secretNames(worker), ["TEMPORAL_API_KEY"]);
});

test("live mode uses the real providers, with their keys and the default timings", () => {
  const { app } = synth("live");
  const worker = container(app, "worker");
  const vars = env(worker);
  assert.equal(vars.LLM_PROVIDER, "anthropic");
  assert.equal(vars.TTS_PROVIDER, "elevenlabs");
  assert.equal(vars.LLM_MODEL, "claude-sonnet-5-5");
  assert.equal(vars.TTS_MODEL, "eleven_v4_turbo");
  for (const timing of ["VOTE_WINDOW", "PREMIERE_COUNTDOWN", "CHOICES_RETRY_WINDOW"]) {
    assert.equal(vars[timing], undefined, `${timing} is left at the worker's default`);
  }
  assert.deepEqual(secretNames(worker), ["ANTHROPIC_API_KEY", "ELEVENLABS_API_KEY", "TEMPORAL_API_KEY"]);
});

test("the web app is given nothing: no variables and no secrets", () => {
  const { app } = synth("live");
  const web = container(app, "web");
  assert.deepEqual(env(web), {});
  assert.deepEqual(secretNames(web), []);
});

test("the cache is one small Valkey 8.1 node, encrypted, reachable only from the server and worker", () => {
  const { app } = synth("test");
  app.resourceCountIs("AWS::ElastiCache::ReplicationGroup", 1);
  app.hasResourceProperties("AWS::ElastiCache::ReplicationGroup", {
    Engine: "valkey",
    EngineVersion: "8.1",
    CacheNodeType: "cache.t4g.micro",
    NumCacheClusters: 1,
    TransitEncryptionEnabled: true,
    AtRestEncryptionEnabled: true,
  });
  // Exactly two ways in on the cache port: from the server's and the worker's security groups.
  const ingress = Object.values(app.findResources("AWS::EC2::SecurityGroupIngress")).filter(
    (r) => r.Properties.FromPort === 6379,
  );
  assert.equal(ingress.length, 2);
  for (const rule of ingress) {
    assert.ok(rule.Properties.SourceSecurityGroupId, "from a security group, not an address range");
  }
});

test("no resource name or description contains an em dash", () => {
  const { data, app } = synth("live");
  assert.doesNotMatch(JSON.stringify(data.toJSON()), /—/);
  assert.doesNotMatch(JSON.stringify(app.toJSON()), /—/);
});
