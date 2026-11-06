// The engine exposes permission requests as select dialogs, not a typed permission frame.
// Require its full dialog shape and tool-call binding; a normal question must stay human.
import { realpathSync } from "node:fs";
import {
	basename,
	dirname,
	isAbsolute,
	join,
	relative,
	resolve,
} from "node:path";

export function isPermissionDialog(dialog) {
	return dialog?.method === "select" &&
		typeof dialog.toolCallId === "string" &&
		typeof dialog.title === "string" &&
		dialog.title.startsWith("Permission required: ") &&
		["Allow once", "Allow always", "Deny", "Deny with feedback"].every(
			(value) => dialog.options?.includes(value),
		);
}

function canonical(path) {
	const missing = [];
	let existing = resolve(path);
	for (;;) {
		try {
			return join(realpathSync(existing), ...missing);
		} catch (error) {
			if (error.code !== "ENOENT") return null;
			const parent = dirname(existing);
			if (parent === existing) return null;
			missing.unshift(basename(existing));
			existing = parent;
		}
	}
}

export function permissionDecision(dialog, access, cwd, call) {
	const [heading, ...lines] = dialog.title.split("\n");
	const tool = heading.slice("Permission required: ".length);
	const targetLine = lines.find((line) =>
		/^(File|Path|Directory|Command): /.test(line)
	);
	const target = targetLine?.replace(/^[^:]+: /, "") ?? "unknown";
	let allow = false;
	// Codemode hides bash from the model. Permit only a literal single-tool bridge,
	// not general JavaScript; the bridged tool still passes through engine permissions.
	if (
		tool === "eval" && access === "workspace-write" &&
		call?.name === "eval" && call.arguments?.language === "js"
	) {
		const code = call.arguments.code;
		const match = typeof code === "string" &&
			code.match(
				/^\s*(?:await\s+)?tool\.(bash|monitor)\((\{[\s\S]*\})\)\s*;?\s*$/,
			);
		if (match) {
			try {
				const args = JSON.parse(match[2]);
				const commands = typeof args.command === "string"
					? args.command.split(/\s*&&\s*/)
					: [];
				const root = canonical(cwd);
				const sameCwd = args.cwd === undefined ||
					canonical(resolve(cwd, args.cwd)) === root;
				allow = Object.keys(args).every((key) =>
					[
						"command",
						"cwd",
						"description",
						"run_in_background",
						"timeout",
					].includes(key)
				) &&
					sameCwd && commands.length > 0 &&
					commands.every((command) =>
						/^(?:ls|git status --short|git add [A-Za-z0-9_][A-Za-z0-9_.-]*|git commit -m (?:[A-Za-z0-9_][A-Za-z0-9_.-]*|"[A-Za-z0-9_. -]+"|'[A-Za-z0-9_. -]+'))$/
							.test(command)
					);
			} catch {
				allow = false;
			}
		}
		return {
			tool,
			target: "literal workspace shell bridge",
			mode: access,
			value: allow ? "Allow once" : "Deny",
		};
	}
	if (
		access === "full" &&
		["edit", "read", "list", "grep", "glob", "bash", "external_directory"]
			.includes(tool)
	) {
		allow = true;
	} else if (
		["edit", "read", "list"].includes(tool) && target !== "unknown" &&
		target !== "Unknown" &&
		!/[\n\r*?\[\]$~]/.test(target) &&
		(tool !== "edit" || access === "workspace-write")
	) {
		const root = canonical(cwd);
		const path = canonical(resolve(cwd, target));
		const step = root && path ? relative(root, path) : null;
		allow = step !== null && !isAbsolute(step) && step !== ".." &&
			!step.startsWith("../");
	}
	// Shell policy is enforced by omo's workspace preset. An unexpected shell ask is not
	// evidence that an arbitrary command is confined; unknown/custom/external requests deny.
	return { tool, target, mode: access, value: allow ? "Allow once" : "Deny" };
}
