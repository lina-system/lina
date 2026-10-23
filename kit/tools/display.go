// SPDX-License-Identifier: Apache-2.0
package tools

// DisplayName returns a trusted activity label for a registered Core tool name.
// Unknown names, including plugin and model-supplied strings, get a fixed label.
func DisplayName(name string) string {
	switch name {
	case "skill_index":
		return "Listing skills"
	case "read_skill":
		return "Reading skill"
	case "write_skill":
		return "Writing skill"
	case "undo_skill":
		return "Restoring skill"
	case "read_file":
		return "Reading file"
	case "write_file":
		return "Writing file"
	case "delete_file":
		return "Deleting file"
	case "restore_file":
		return "Restoring file"
	case "apply_patch":
		return "Editing files"
	case "exec_command":
		return "Running command"
	case "memory_write":
		return "Saving memory"
	case "recall_person":
		return "Recalling context"
	case "expand":
		return "Reading context"
	case "present_result", "present_surface":
		return "Presenting result"
	case "read_result":
		return "Reading result"
	case "save_result":
		return "Saving result"
	case "change_persona":
		return "Updating persona"
	case "undo_persona":
		return "Restoring persona"
	case "fill_persona_setting":
		return "Updating persona setting"
	case "record_relationship":
		return "Updating relationship"
	case "record_emotion":
		return "Recording emotion"
	case "set_emotion_expression":
		return "Updating expression"
	case "growth_notices":
		return "Reading growth notices"
	case "undo_growth":
		return "Restoring growth"
	case "start_run":
		return "Starting work"
	case "dispatch_work":
		return "Delegating work"
	default:
		return "Using tool"
	}
}
