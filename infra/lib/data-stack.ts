import { CfnOutput, Duration, RemovalPolicy, Stack, StackProps } from "aws-cdk-lib";
import * as dsql from "aws-cdk-lib/aws-dsql";
import * as iam from "aws-cdk-lib/aws-iam";
import * as s3 from "aws-cdk-lib/aws-s3";
import * as secretsmanager from "aws-cdk-lib/aws-secretsmanager";
import { Construct } from "constructs";

/**
 * What is kept between demos: the database, the bucket, the secrets, and the
 * two task roles. It costs almost nothing while nothing is running.
 *
 * The task roles are here, and not with the tasks, because the database maps
 * its application role to their ARNs (`migrate grant`). If they were made
 * with the tasks, every teardown would break that mapping.
 */
export class DataStack extends Stack {
  readonly clusterEndpoint: string;
  readonly bucket: s3.Bucket;
  readonly serverRole: iam.Role;
  readonly workerRole: iam.Role;
  readonly shareLinkSecret: secretsmanager.Secret;
  readonly temporalApiKey: secretsmanager.Secret;
  readonly anthropicApiKey: secretsmanager.Secret;
  readonly elevenLabsApiKey: secretsmanager.Secret;

  constructor(scope: Construct, id: string, props: StackProps) {
    super(scope, id, { ...props, description: "Monster Theatre - database, bucket, secrets, task roles" });

    // Aurora DSQL: nothing to size, and no password. Deletion protection
    // has to be turned off by hand before the stack can be destroyed.
    const cluster = new dsql.CfnCluster(this, "Database", {
      deletionProtectionEnabled: true,
      tags: [{ key: "Name", value: "monster-theatre" }],
    });
    this.clusterEndpoint = cluster.attrEndpoint;

    // Portraits and recordings. Browsers never reach it: the server serves
    // both through its own authorised endpoints. Everything in it may be
    // deleted, which the seven-day rule does anyway.
    this.bucket = new s3.Bucket(this, "Objects", {
      blockPublicAccess: s3.BlockPublicAccess.BLOCK_ALL,
      encryption: s3.BucketEncryption.S3_MANAGED,
      enforceSSL: true,
      lifecycleRules: [{ expiration: Duration.days(7) }],
      removalPolicy: RemovalPolicy.DESTROY,
      autoDeleteObjects: true,
    });

    // The three keys are created holding a random placeholder. Their real
    // values are put in by hand (README, "Deploying"), and are never part
    // of a template, a task definition, or this repository.
    const placeholder = (id: string, name: string, description: string) =>
      new secretsmanager.Secret(this, id, { secretName: `monster-theatre/${name}`, description });
    this.anthropicApiKey = placeholder("AnthropicApiKey", "anthropic-api-key", "Anthropic API key, read by the worker");
    this.elevenLabsApiKey = placeholder("ElevenLabsApiKey", "elevenlabs-api-key", "ElevenLabs API key, read by the worker");
    this.temporalApiKey = placeholder("TemporalApiKey", "temporal-api-key", "Temporal Cloud API key, read by the server and the worker");

    // Share links are derived from this one. Nobody needs to know it, so it
    // is generated here. Replacing it invalidates every share link (D034).
    this.shareLinkSecret = new secretsmanager.Secret(this, "ShareLinkSecret", {
      secretName: "monster-theatre/share-link-secret",
      description: "Key that share links are derived from, read by the server",
      generateSecretString: { passwordLength: 64, excludePunctuation: true },
    });

    // Exported whatever the mode. The app stack reads the provider keys
    // only in live mode; were these exported only then, switching a running
    // deployment back to test would mean removing exports still in use,
    // which CloudFormation refuses.
    this.exportValue(this.anthropicApiKey.secretArn);
    this.exportValue(this.elevenLabsApiKey.secretArn);

    const taskRole = (id: string, description: string) =>
      new iam.Role(this, id, { assumedBy: new iam.ServicePrincipal("ecs-tasks.amazonaws.com"), description });
    this.serverRole = taskRole("ServerTaskRole", "Monster Theatre server task");
    this.workerRole = taskRole("WorkerTaskRole", "Monster Theatre worker task");

    // DbConnect lets a role sign in as a database role mapped to it, which
    // is the application's role. DbConnectAdmin, the schema owner's, is
    // given to neither.
    for (const role of [this.serverRole, this.workerRole]) {
      role.addToPolicy(new iam.PolicyStatement({ actions: ["dsql:DbConnect"], resources: [cluster.attrResourceArn] }));
    }

    // The server stores portraits and serves portraits and recordings; the
    // worker only stores recordings.
    this.bucket.grantRead(this.serverRole);
    this.bucket.grantPut(this.serverRole, "rooms/*");
    this.bucket.grantPut(this.workerRole, "performances/*");

    new CfnOutput(this, "DsqlEndpoint", { value: this.clusterEndpoint });
    new CfnOutput(this, "BucketName", { value: this.bucket.bucketName });
    new CfnOutput(this, "ServerRoleArn", { value: this.serverRole.roleArn });
    new CfnOutput(this, "WorkerRoleArn", { value: this.workerRole.roleArn });
  }
}
