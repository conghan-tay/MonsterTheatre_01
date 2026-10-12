import * as fs from "node:fs";

/** The settings that are yours and not secret. They live in config.json. */
export interface Config {
  readonly region: string;
  /** The Temporal Cloud namespace's endpoint, as its page shows it, with the port. */
  readonly temporalAddress: string;
  /** The namespace, including the account part: "name.a1b2c". */
  readonly temporalNamespace: string;
  /** The two ElevenLabs voices, recorded on each room when it is created. */
  readonly voiceIdA: string;
  readonly voiceIdB: string;
  readonly llmModel: string;
  readonly ttsModel: string;
}

/**
 * "test" runs the stand-in providers with the short timings the end-to-end
 * suite expects, and spends nothing. "live" runs Claude and ElevenLabs.
 */
export type Mode = "test" | "live";

const keys: (keyof Config)[] = [
  "region",
  "temporalAddress",
  "temporalNamespace",
  "voiceIdA",
  "voiceIdB",
  "llmModel",
  "ttsModel",
];

export function loadConfig(path: string): Config {
  if (!fs.existsSync(path)) {
    throw new Error(`${path} not found: copy config.example.json to config.json and fill it in`);
  }
  const raw = JSON.parse(fs.readFileSync(path, "utf8")) as Record<string, unknown>;
  for (const key of keys) {
    if (typeof raw[key] !== "string" || raw[key] === "") {
      throw new Error(`${path}: "${key}" must be a non-empty string`);
    }
  }
  return raw as unknown as Config;
}

export function parseMode(value: unknown): Mode {
  if (value === undefined) return "test";
  if (value === "test" || value === "live") return value;
  throw new Error(`mode must be "test" or "live", not ${JSON.stringify(value)}`);
}
