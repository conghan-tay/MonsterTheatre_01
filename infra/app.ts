#!/usr/bin/env node
import * as path from "node:path";
import { App } from "aws-cdk-lib";

import { loadConfig, parseMode } from "./lib/config";
import { AppStack } from "./lib/app-stack";
import { DataStack } from "./lib/data-stack";

// cdk deploy [-c mode=live] [-c config=other.json]
//
// mode defaults to "test": stand-in providers, nothing spent on Claude or
// ElevenLabs. Real providers must be asked for.
const app = new App();
const config = loadConfig(path.resolve(__dirname, app.node.tryGetContext("config") ?? "config.json"));
const mode = parseMode(app.node.tryGetContext("mode"));
const env = { account: process.env.CDK_DEFAULT_ACCOUNT, region: config.region };

const data = new DataStack(app, "MonsterTheatreData", { env, terminationProtection: true });
new AppStack(app, "MonsterTheatreApp", { env, data, config, mode });
