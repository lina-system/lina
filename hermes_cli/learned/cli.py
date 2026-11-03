"""Argument parsing and command dispatch for lina learned."""
import argparse
from datetime import datetime, timedelta
import json
import sys
from hermes_cli.learned import storage, config as config_module
from hermes_cli.learned.collector import collect, CollectionError, SOURCE_ERRORS
from hermes_cli.work.common import mask
def main(argv=None):
    parser = argparse.ArgumentParser(description="Review masked learning records; only card --send delivers")
    commands = parser.add_subparsers(dest="command", required=True)
    commands.add_parser("collect")
    for name in ("fold", "card", "json"):
        command = commands.add_parser(name)
        command.add_argument("--day", required=True)
        if name == "card":
            command.add_argument("--llm", action="store_true")
            command.add_argument("--send", action="store_true")
    command = commands.add_parser("reply")
    command.add_argument("text")
    command = commands.add_parser("verdict", usage="learned verdict (<item> | --day D --line N) ok|fix|drop [--text TEXT]")
    command.add_argument("words", nargs="+", metavar="[item] verdict")
    command.add_argument("--day")
    command.add_argument("--line", type=int)
    command.add_argument("--text")
    args = parser.parse_args(argv)
    if args.command == "verdict":
        by_line = args.day is not None or args.line is not None
        if (by_line and (args.day is None or args.line is None or len(args.words) != 1)
                or not by_line and len(args.words) != 2 or args.words[-1] not in ("ok", "fix", "drop")):
            command.error("give <item> ok|fix|drop, or --day D --line N ok|fix|drop")
    if getattr(args, "day", None) == "yesterday":
        args.day = (datetime.now() - timedelta(days=1)).strftime("%Y-%m-%d")
    try:
        if args.command == "collect":
            collect()
        elif args.command in ("reply", "verdict"):
            from hermes_cli.learned import reply as replies
            config = config_module.Config()
            if args.command == "reply":
                code, line = replies.reply(config, args.text, storage, mask)
            elif args.line is not None:
                code, line = replies.verdict_line(config, args.day, args.line, args.words[-1], args.text, storage, mask)
            else:
                code, line = replies.verdict(config, args.words[0], args.words[-1], args.text, storage, mask)
            print(line, file=sys.stderr if code else sys.stdout)
            return code
        else:
            config = config_module.Config()
            from hermes_cli.learned import fold
            from hermes_cli.learned import card
            view = fold.build(config, args.day)
            view["card"], view["card_order"] = card.render(
                view, fold.display.phrase, config.llm_cmd if getattr(args, "llm", False) else "")
            if args.command == "fold":
                fold.write(config, view)
                print("learned: folded masked items")
            elif getattr(args, "send", False):
                from hermes_cli.learned import send as sender
                folded = {row["id"]: row for row in view["folded"]}
                view["card_lines"] = [dict(n=n, id=id_, ids=folded[id_]["members"])
                                      for n, id_ in enumerate(view["card_order"], 1)]
                print(view["card"])
                try:
                    return sender.send(config, view, mask)
                except sender.SendNotConfigured:
                    print("learned: set learned.send_target in the root config.yaml", file=sys.stderr)
                    return 1
            elif args.command == "card":
                print(view["card"])
            else:
                # The card the owner saw, frozen at sending; card_order follows today's items.
                sent = storage.read_json(config.state / "cards" / (args.day + ".json"), None)
                if sent and sent.get("day") == args.day:
                    view["sent_lines"] = sent["lines"]
                print(json.dumps(view, ensure_ascii=False, sort_keys=True))
            return 0
    except config_module.MissingProfiles as error:
        print("\n".join(error.names), file=sys.stderr)
        return 1
    except CollectionError as error:
        print(f"learned: collection failed ({error.stage}: {error.error_class}); check source access, format, and configuration", file=sys.stderr)
        return 1
    except SOURCE_ERRORS:
        # Source exceptions may contain private paths, SQL values, or credentials.
        print("learned: command failed; check source access, format, and configuration", file=sys.stderr)
        return 1
    print("learned: collected masked items")
    return 0
