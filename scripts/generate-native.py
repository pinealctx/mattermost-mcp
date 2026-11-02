"""Generate the remote CLI parameter index from the pinned public Agents release."""
import hashlib
import json
import re
import subprocess
import sys
from pathlib import Path

REVISION = "f81a90b2b0c0b388d19a81ade9772b2506cf8f9a"
root = Path(sys.argv[1])
# Archives may lack Git metadata. Use a verified checkout when updating the index.
if (root / ".git").exists():
    assert subprocess.check_output(["git", "-C", str(root), "rev-parse", "HEAD"], text=True).strip() == REVISION
source = "\n".join(p.read_text() for p in sorted((root / "mcpserver/tools").glob("*.go")) if not p.name.endswith("_test.go"))
if hashlib.sha256(source.encode()).hexdigest() != "ab38e9a564a98bcb1fc23324182294c3011f9c129d531975392ea0ae6a22a926":
    raise ValueError(f"Tool sources differ from the pinned public commit {REVISION}")
structs = dict(re.findall(r"(?m)^type (\w+) struct\s*\{([^}]*?)\}", source))
handlers = dict(re.findall(r"func \(p \*MattermostToolProvider\) (tool\w+)\(\w+ \*MCPToolContext, \w+ (\w+)\)", source))
catalog = {}

def add(name, struct, readonly):
    flags = {}
    for line in structs[struct].splitlines():
        if 'json:"' not in line or 'access:"local"' in line:
            continue
        match = re.search(r'\w+\s+(\S+)\s+`json:"([^",]+)', line)
        if not match:
            raise ValueError((name, line))
        typ, field = match.groups()
        typ = typ.lstrip("*")
        flags[field] = typ if typ in {"string", "bool", "int", "int64"} else "json"
    catalog[name] = {"fields": flags, "read": readonly}

for readonly, name, handler in re.findall(r'mcp(Read)?Tool\(p,\s*"([^"]+)",[^\n]*?,\s*p\.(\w+)\)', source):
    add(name, handlers[handler], bool(readonly))
# The search tool constructs its schema according to server capabilities.
add("search_posts", "CombinedSearchArgs", True)
if len(catalog) != 121:
    raise ValueError(f"Unexpected number of registrations: {len(catalog)}")
dest = Path(__file__).resolve().parents[1] / "internal/native/tools.json"
dest.write_text(json.dumps(catalog, sort_keys=True, indent=2) + "\n")
