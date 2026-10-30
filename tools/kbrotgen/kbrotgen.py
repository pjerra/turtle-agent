#!/usr/bin/env python3
"""Generates the knowledge-base rotations (internal/brain/rotations/kb_*.json) and the spells oracle
(internal/kbspells/oracle/<class>.json) from hand-written recipes and the Turtle knowledge base's YAML.

  python3 -I tools/kbrotgen/kbrotgen.py --kb ~/TortoiseBots-audit/turtle-kb [--only NAME] [--check]

A recipe (tools/kbrotgen/recipes/<name>.json) maps every element of the KB file's rotation_single, rotation_aoe and
cooldowns to zero or more rotation entries or to an explicit unexpressed reason; it is the human judgment and the only
hand-written part. The generator checks that every KB row has a recipe row and the other way round, that the row text is
unchanged (text_sha = first 12 hex of sha256 of the whitespace-collapsed text), that `order` names every entry once, and
that the list has at most 40 entries. It then writes the rotation file and the class oracle with the KB credit and the
KB file's own sources.

The rotation JSON is written in the Go struct order (internal/brain/rotation.go), two-space indent, trailing newline, so
the bytes are the same on every run. `--check` regenerates in memory and exits 1 with a diff summary when a committed
file differs; it writes nothing.

The recipe format is the one in the spec (turtle-agent-kb-work.md §2.3); an entry is a Go rotation Priority object, its
`when` a Go Conditions object. This script deliberately does not know the spell data: `turtle-agent rotation check` is
the check for names, levels and ranks.
"""
import argparse
import difflib
import hashlib
import json
import os
import re
import sys
from pathlib import Path

import yaml

KB_NAME = "Turtle WoW knowledge base (stevemcqueenz)"
KB_URL = "https://stevemcqueenz.github.io/turtle-knowledge-base-/#/"
SECTIONS = ("rotation_single", "rotation_aoe", "cooldowns")
MAX_ENTRIES = 40
YAML_LIMIT = 4 * 1024 * 1024

# The Go struct order (internal/brain/rotation.go). A key not here is a recipe mistake (Go would refuse the file).
PRIORITY_KEYS = ("id", "kind", "spell", "spell_id", "level", "talent", "rank", "target", "band", "threat",
                 "harmful", "alt", "stances", "when", "note")
CONDITION_KEYS = ("state", "melee_mob", "mob_in_reach", "mobs_in_reach_min", "mobs_in_cone_min", "mob_distance_min",
                  "mob_distance_max", "mob_on_me", "tank_holds", "feared", "mob_fleeing", "moving", "in_combat",
                  "after", "combo_points_min", "stance", "self_hp_below", "self_hp_above", "power_below",
                  "power_above", "mob_hp_below", "mob_hp_above", "self_aura", "no_self_aura",
                  "self_aura_missing", "mob_aura", "no_mob_aura", "mob_stacks_below", "mobs_lacking_aura",
                  "level_min", "level_max", "mobs_near_mob_min", "heal_hp_below", "heal_is_tank", "heal_no_aura",
                  "members_hurt_min")
RECIPE_KEYS = ("name", "class", "spec", "role", "kb_file", "preset", "replaces", "mode", "notes", "rows", "order",
               "oracle")
ROW_KEYS = ("from", "index", "priority", "text", "text_sha", "entries", "unexpressed")
ORACLE_KEYS = ("core", "situational", "never", "signature", "taunt")
NAME = re.compile(r"^[a-z0-9_]{1,40}$")


def norm(text):
    return " ".join(str(text).split())


def row_text(section, row):
    """The text of a KB row: action | condition, or name | use_when for a cooldown. rotation_aoe rows carry no
    condition, so their text is the action."""
    if section == "cooldowns":
        return norm("%s | %s" % (row.get("name", ""), row.get("use_when", "")))
    if section == "rotation_single":
        return norm("%s | %s" % (row.get("action", ""), row.get("condition", "")))
    return norm(row.get("action", ""))


def text_sha(text):
    return hashlib.sha256(text.encode("utf8")).hexdigest()[:12]


def load_kb(kb_dir, rel):
    path = (Path(kb_dir) / rel).resolve()
    root = Path(kb_dir).resolve()
    if os.path.commonpath([str(root), str(path)]) != str(root):
        raise ValueError("kb_file %r leaves the knowledge base" % rel)
    data = path.read_bytes()
    if len(data) > YAML_LIMIT:
        raise ValueError("kb_file %r is larger than %d bytes" % (rel, YAML_LIMIT))
    return data, yaml.safe_load(data.decode("utf8"))


def kb_rows(doc):
    """Every row of the three sections, by (section, index), with its text."""
    out = {}
    for section in SECTIONS:
        rows = doc.get(section) or []
        if not isinstance(rows, list):
            raise ValueError("%s: not a list" % section)
        for i, row in enumerate(rows):
            if not isinstance(row, dict):
                raise ValueError("%s[%d]: not a mapping" % (section, i))
            out[(section, i)] = (row, row_text(section, row))
    return out


def ordered_conditions(when):
    return {k: when[k] for k in CONDITION_KEYS if k in when}


def ordered_priority(entry):
    out = {"id": entry["id"]}
    for key in PRIORITY_KEYS[1:]:
        if key == "when":
            out["when"] = ordered_conditions(entry.get("when") or {})
        elif key in entry and entry[key] is not None:
            out[key] = entry[key]
    return out


def dumps(obj):
    return json.dumps(obj, indent=2, ensure_ascii=False) + "\n"


def key_text(key):
    return "%s[%d]" % key


def validate(recipe, rows):
    """Every problem of one recipe, in the order a reader would want them."""
    name = recipe.get("name") or "?"
    problems = []

    def bad(msg):
        problems.append("%s: %s" % (name, msg))

    for key in recipe:
        if key not in RECIPE_KEYS:
            bad("unknown recipe key %r" % key)
    if not isinstance(recipe.get("name"), str) or not NAME.match(recipe.get("name") or ""):
        bad("name %r: use a-z, 0-9 and _" % recipe.get("name"))
    for key in ("class", "spec", "role", "kb_file", "preset"):
        if not isinstance(recipe.get(key), str) or not recipe.get(key):
            bad("%s: a non-empty string" % key)
    if recipe.get("mode") not in ("", "add"):
        bad("mode %r: use add or nothing" % recipe.get("mode"))
    repl = recipe.get("replaces")
    if not isinstance(repl, list) or not all(isinstance(s, str) and s for s in repl):
        bad("replaces: a list of strategy names")
        repl = []
    if recipe.get("mode") == "add" and repl:
        bad("mode add: replaces must be empty")
    if recipe.get("mode") != "add" and not repl:
        bad("replaces: name the strategies this takes the place of")
    if not isinstance(recipe.get("notes"), str) or not recipe.get("notes"):
        bad("notes: say what the list plays")

    rrows = recipe.get("rows")
    if not isinstance(rrows, list) or not rrows:
        bad("rows: a non-empty list")
        rrows = []
    by_key = {}
    entries = {}
    for row in rrows:
        if not isinstance(row, dict):
            bad("a row is not a mapping")
            continue
        for key in row:
            if key not in ROW_KEYS:
                bad("row: unknown key %r" % key)
        section, index = row.get("from"), row.get("index")
        if section not in SECTIONS:
            bad("row from %r: use %s" % (section, ", ".join(SECTIONS)))
            continue
        if not isinstance(index, int) or index < 0:
            bad("row index %r: 0 or above" % (index,))
            continue
        key = (section, index)
        if key in by_key:
            bad("%s: the recipe has this row twice" % key_text(key))
            continue
        by_key[key] = row
        if not isinstance(row.get("text"), str):
            bad("%s: text" % key_text(key))
        if not isinstance(row.get("text_sha"), str):
            bad("%s: text_sha" % key_text(key))
        ents = row.get("entries") or []
        if not isinstance(ents, list):
            bad("%s: entries: a list" % key_text(key))
            ents = []
        unexpr = row.get("unexpressed", "")
        if unexpr and not isinstance(unexpr, str):
            bad("%s: unexpressed: a string" % key_text(key))
        if not ents and not unexpr:
            bad("%s: the row maps to nothing (entries or unexpressed)" % key_text(key))
        for ent in ents:
            if not isinstance(ent, dict):
                bad("%s: an entry is not a mapping" % key_text(key))
                continue
            for k in ent:
                if k not in PRIORITY_KEYS:
                    bad("%s: entry key %r is not a rotation field" % (key_text(key), k))
            when = ent.get("when")
            if when is not None and not isinstance(when, dict):
                bad("%s: entry when: a mapping" % key_text(key))
            if isinstance(when, dict):
                for k in when:
                    if k not in CONDITION_KEYS:
                        bad("%s: when key %r is not a condition" % (key_text(key), k))
            eid = ent.get("id")
            if not isinstance(eid, str) or not NAME.match(eid):
                bad("%s: entry id %r: use a-z, 0-9 and _" % (key_text(key), eid))
                continue
            if eid in entries:
                bad("%s: entry id %r is used twice" % (key_text(key), eid))
            entries[eid] = ent

    # Every KB row and every recipe row, both ways.
    for key in rows:
        if key not in by_key:
            bad("%s: the KB row has no recipe row" % key_text(key))
    for key, row in by_key.items():
        if key not in rows:
            bad("%s: the recipe row has no KB row" % key_text(key))
            continue
        kb_row, kb_text = rows[key]
        if row.get("text") != kb_text:
            bad("%s: text differs: recipe %r, KB %r" % (key_text(key), row.get("text"), kb_text))
        want = text_sha(kb_text)
        if row.get("text_sha") != want:
            bad("%s: text_sha differs: the KB row changed; re-read it and set %r" % (key_text(key), want))

    order = recipe.get("order")
    if not isinstance(order, list) or not all(isinstance(s, str) for s in order):
        bad("order: a list of entry ids")
        order = []
    if len(order) > MAX_ENTRIES:
        bad("%d entries, more than %d" % (len(order), MAX_ENTRIES))
    seen = set()
    for eid in order:
        if eid in seen:
            bad("order: %r appears twice" % eid)
        seen.add(eid)
        if eid not in entries:
            bad("order: %r is in no recipe row" % eid)
    for eid in entries:
        if eid not in seen:
            bad("entry %r is in no order" % eid)

    oracle = recipe.get("oracle")
    if not isinstance(oracle, dict):
        bad("oracle: a mapping")
    else:
        for key in oracle:
            if key not in ORACLE_KEYS:
                bad("oracle: unknown key %r" % key)
        for key in ORACLE_KEYS:
            v = oracle.get(key)
            if not isinstance(v, list) or not all(isinstance(s, str) and s for s in v):
                bad("oracle.%s: a list of spell names" % key)
        core = oracle.get("core") or []
        if not 1 <= len(core) <= 5:
            bad("oracle.core: 1-5 spells")
        sig = oracle.get("signature") or []
        if not 1 <= len(sig) <= 3:
            bad("oracle.signature: 1-3 spells")
    return problems, entries, order


def build_rotation(recipe, entries, order, doc, kb_bytes):
    name = recipe["name"]
    sources = ["%s, %s, %s" % (KB_NAME, recipe["kb_file"], KB_URL)]
    seen = {sources[0]}
    for src in doc.get("sources") or []:
        if not isinstance(src, dict):
            continue
        s = "%s (%s, %s, %s)" % (src.get("url", ""), src.get("author", ""), src.get("authority", ""),
                                 src.get("date", ""))
        if s not in seen:
            seen.add(s)
            sources.append(s)

    unexpressed = []
    for row in recipe["rows"]:
        why = row.get("unexpressed")
        if not why:
            continue
        unexpressed.append({"row": key_text((row["from"], row["index"])), "text": row.get("text", ""), "why": why})

    rot = {"version": 1, "name": name, "class": recipe["class"]}
    if recipe.get("spec"):
        rot["spec"] = recipe["spec"]
    if recipe.get("mode"):
        rot["mode"] = recipe["mode"]
    rot["replaces"] = list(recipe["replaces"])
    rot["notes"] = recipe["notes"]
    rot["sources"] = sources
    rot["priorities"] = [ordered_priority(entries[eid]) for eid in order]
    kb = {"file": recipe["kb_file"], "sha256": hashlib.sha256(kb_bytes).hexdigest(), "preset": recipe["preset"],
          "recipe": "tools/kbrotgen/recipes/%s.json" % name}
    if unexpressed:
        kb["unexpressed"] = unexpressed
    rot["kb"] = kb
    return rot


def build_oracle(cls, recipes):
    specs = []
    for recipe in sorted(recipes, key=lambda r: r["spec"]):
        o = recipe["oracle"]
        specs.append({"spec": recipe["spec"], "role": recipe["role"], "rotation": recipe["name"],
                      "core": list(o["core"]), "situational": list(o["situational"]), "never": list(o["never"]),
                      "signature": list(o["signature"]), "taunt": list(o["taunt"])})
    return {"class": cls, "specs": specs}


def load_recipes(root):
    dir = Path(root) / "tools" / "kbrotgen" / "recipes"
    out = []
    for path in sorted(dir.glob("*.json")):
        try:
            recipe = json.loads(path.read_text(encoding="utf8"))
        except (OSError, json.JSONDecodeError) as e:
            raise ValueError("%s: %s" % (path, e))
        if not isinstance(recipe, dict):
            raise ValueError("%s: the recipe is not a JSON object" % path)
        out.append((path, recipe))
    return out


def generate(kb_dir, root, only=None, check=False, out=None, err=None):
    out = out or sys.stdout
    err = err or sys.stderr
    try:
        recipes = load_recipes(root)
    except ValueError as e:
        print("kbrotgen: %s" % e, file=err)
        return 1
    if not recipes:
        print("kbrotgen: no recipes in %s/tools/kbrotgen/recipes" % root, file=err)
        return 1
    if only is not None and not any(rec.get("name") == only for _, rec in recipes):
        print("kbrotgen: no recipe named %r" % only, file=err)
        return 1

    problems = []
    built = []          # (recipe, rotation dict)
    by_class = {}       # class -> [recipe]
    for path, recipe in recipes:
        name = recipe.get("name") or path.stem
        try:
            kb_bytes, doc = load_kb(kb_dir, recipe.get("kb_file", ""))
        except (OSError, UnicodeDecodeError, ValueError, yaml.YAMLError) as e:
            problems.append("%s: %s" % (name, e))
            continue
        if not isinstance(doc, dict):
            problems.append("%s: the KB file is not a mapping" % name)
            continue
        try:
            rows = kb_rows(doc)
        except ValueError as e:
            problems.append("%s: %s" % (name, e))
            continue
        p, entries, order = validate(recipe, rows)
        problems.extend(p)
        if p:
            continue
        built.append((recipe, build_rotation(recipe, entries, order, doc, kb_bytes)))
        by_class.setdefault(recipe["class"], []).append(recipe)

    if problems:
        for p in problems:
            print(p, file=err)
        return 1

    # The oracle of a class always holds every recipe of that class: a --only run must not drop the others.
    oracles = {}
    for cls, recs in by_class.items():
        specs = [r["spec"] for r in recs]
        if len(set(specs)) != len(specs):
            print("kbrotgen: class %s has two recipes for one spec" % cls, file=err)
            return 1
        oracles[cls] = build_oracle(cls, recs)

    wanted = []
    for recipe, rot in built:
        if only is not None and recipe["name"] != only:
            continue
        wanted.append(((Path(root) / "internal" / "brain" / "rotations" / (recipe["name"] + ".json")),
                       dumps(rot)))
    classes = {rec["class"] for rec, _ in built if only is None or rec["name"] == only}
    for cls in sorted(classes):
        wanted.append(((Path(root) / "internal" / "kbspells" / "oracle" / (cls + ".json")),
                       dumps(oracles[cls])))

    if check:
        bad = 0
        for path, content in wanted:
            try:
                old = path.read_text(encoding="utf8")
            except OSError:
                print("%s: missing" % path, file=out)
                bad += 1
                continue
            if old == content:
                continue
            bad += 1
            print("%s: differs from the recipes" % path, file=out)
            diff = list(difflib.unified_diff(old.splitlines(), content.splitlines(), "committed", "generated",
                                             lineterm=""))
            for line in diff[:30]:
                print("  " + line, file=out)
            if len(diff) > 30:
                print("  ... %d more line(s)" % (len(diff) - 30), file=out)
        return 1 if bad else 0

    for path, content in wanted:
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(content, encoding="utf8", newline="\n")
    print("kbrotgen: wrote %d file(s)" % len(wanted), file=out)
    return 0


def main(argv=None):
    here = Path(__file__).resolve().parents[2]
    p = argparse.ArgumentParser(description="generate the KB rotations and the spells oracle")
    p.add_argument("--kb", required=True, help="the knowledge base root (classes/yaml/...)")
    p.add_argument("--only", help="one recipe name")
    p.add_argument("--check", action="store_true", help="regenerate in memory and fail on a difference")
    p.add_argument("--root", default=str(here), help=argparse.SUPPRESS)
    a = p.parse_args(argv)
    return generate(a.kb, a.root, only=a.only, check=a.check)


if __name__ == "__main__":
    sys.exit(main())
