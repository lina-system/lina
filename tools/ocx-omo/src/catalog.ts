import { existsSync, readFileSync } from "node:fs";
import { OcxOmoError } from "./errors";

export type CatalogModel = { id: string; thinkingLevelMap?: Record<string, string | null> };
export type Catalog = Map<string, CatalogModel>;

type ModelsFile = { providers?: Record<string, { models?: CatalogModel[] }> };

export function loadCatalog(path: string, provider: string): Catalog {
  if (!existsSync(path)) throw new OcxOmoError(`models file not found: ${path}`);
  const parsed = Bun.JSONC.parse(readFileSync(path, "utf8")) as ModelsFile;
  const models = parsed.providers?.[provider]?.models;
  if (!models?.length) {
    throw new OcxOmoError(
      `${path} has no ${provider} models; run \`ocx integration client enable --client omo\``,
    );
  }
  return new Map(models.map((model) => [model.id, model]));
}
