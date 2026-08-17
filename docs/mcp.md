# Docs over MCP

`bosun mcp` serves the Bosun documentation to AI coding tools over the Model Context Protocol, so an assistant can search and read the docs while it works. It speaks JSON-RPC over stdin/stdout, the MCP stdio transport, and serves the same embedded documentation as the website, which means it works offline and always matches the binary you installed.

## What it exposes

The server exposes every documentation page as an MCP **resource** (one per page, under the `bosun://docs/` URI scheme) and provides three **tools**.

| Tool | Argument | Returns |
| --- | --- | --- |
| `search_docs` | `query` | Ranked pages with snippets. |
| `get_doc` | `slug` | The full Markdown of one page. |
| `list_topics` | (none) | Every page's slug and title. |

## Installing into a tool

Every client launches the same command, `bosun mcp`, and talks to it over stdio. What differs between tools is where the configuration lives and its format. The examples below register the server under the name `bosun-docs`; make sure `bosun` is on your `PATH` (or use its absolute path).

### Claude Code

Use the CLI to add the server. This registers it for the current project.

```bash
claude mcp add bosun-docs -- bosun mcp
```

### Claude Desktop

Edit the Claude Desktop config file (`claude_desktop_config.json`, reachable from Settings, Developer) and add an entry under `mcpServers`.

```json
{
  "mcpServers": {
    "bosun-docs": {
      "command": "bosun",
      "args": ["mcp"]
    }
  }
}
```

### Codex CLI

Add a server table to the Codex config (`~/.codex/config.toml`).

```toml
[mcp_servers.bosun-docs]
command = "bosun"
args = ["mcp"]
```

### Cursor

Create `.cursor/mcp.json` in the project (or `~/.cursor/mcp.json` for all projects).

```json
{
  "mcpServers": {
    "bosun-docs": {
      "command": "bosun",
      "args": ["mcp"]
    }
  }
}
```

### VS Code

Create `.vscode/mcp.json` in the workspace.

```json
{
  "servers": {
    "bosun-docs": {
      "type": "stdio",
      "command": "bosun",
      "args": ["mcp"]
    }
  }
}
```

### Windsurf

Add an entry under `mcpServers` in the Windsurf MCP config (`~/.codeium/windsurf/mcp_config.json`).

```json
{
  "mcpServers": {
    "bosun-docs": {
      "command": "bosun",
      "args": ["mcp"]
    }
  }
}
```

### Any other MCP client

The invariant across every tool is the same: run the command `bosun` with the single argument `mcp` as a stdio MCP server. Configuration file locations and key names change between tools and versions, but that launch command does not, so consult your tool's MCP documentation and point it at `bosun mcp`.

## Verifying

After configuring a tool, restart it and confirm the `bosun-docs` server is connected in its MCP or tools panel. You can also exercise the server by hand: it reads one JSON-RPC request per line on stdin and writes one response per line on stdout.

```bash
printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"tools/list"}' | bosun mcp
```
