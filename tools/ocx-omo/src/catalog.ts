import { existsSync, readFileSync } from "node:fs";
import { OcxOmoError } from "./errors.js";
import { parseJsonc } from "./jsonc.js";

export type CatalogModel = { id: string; thinkingLevelMap?: Record<string, string | null> };
export type Catalog = Map<string, CatalogModel>;

type ModelsFile = { providers?: Record<string, { models?: CatalogModel[] }> };

export function loadCatalog(path: string): Catalog {
  if (!existsSync(path)) throw new OcxOmoError(`models file not found: ${path}`);
  const parsed = parseJsonc(readFileSync(path, "utf8"), path) as ModelsFile;
  const models = parsed.providers?.["magpie"]?.models;
  if (!models?.length) {
    throw new OcxOmoError(`${path} has no magpie models; run magpie's connection command for omo`);
  }
  return new Map(models.map((model) => [model.id, model]));
}
