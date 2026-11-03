import { parse, printParseErrorCode, type ParseError } from "jsonc-parser";
import { OcxOmoError } from "./errors.js";

/** JSONC as omo reads it: comments and trailing commas allowed; any syntax error fails. */
export function parseJsonc(text: string, source: string): unknown {
  const errors: ParseError[] = [];
  const value: unknown = parse(text, errors, { allowTrailingComma: true });
  const first = errors[0];
  if (first) throw new OcxOmoError(`${source}: ${printParseErrorCode(first.error)} at offset ${first.offset}`);
  return value;
}
