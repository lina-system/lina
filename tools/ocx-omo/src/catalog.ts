import { existsSync, readFileSync } from "node:fs";
import { OcxOmoError } from "./errors.js";
import { parseJsonc } from "./jsonc.js";

export type CatalogModel = { id: string; thinkingLevelMap?: Record<string, string | null> };
export type Catalog = Map<string, CatalogModel>;

type ModelsFile = { providers?: Record<string, { models?: CatalogModel[] }> };

const OCX_HINT = "run `ocx integration client enable --client omo`";

/** `hint`: how the provider's models get into the file (OCX's integration by default; magpie's own command). */
export function loadCatalog(path: string, provider: string, hint: string = OCX_HINT): Catalog {
  if (!existsSync(path)) throw new OcxOmoError(`models file not found: ${path}`);
  const parsed = parseJsonc(readFileSync(path, "utf8"), path) as ModelsFile;
  const models = parsed.providers?.[provider]?.models;
  if (!models?.length) {
    throw new OcxOmoError(`${path} has no ${provider} models; ${hint}`);
  }
  return new Map(models.map((model) => [model.id, model]));
}
