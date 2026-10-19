# Publishing Dibs to the MCP Registry

The root [`server.json`](../server.json) prepares the remote-only listing
`dev.hivecommons/dibs`, using the namespace approved in
[issue #375](https://github.com/hivecommons/dibs/issues/375#issuecomment-6215095421).
Committing this file does not publish it; publication happens through the
`MCP Registry` workflow described below.

The listing is titled **Dibs (Hive Commons)** to distinguish it from the
unrelated 1stDibs marketplace. Version `0.1.0` corresponds to the MCP
implementation's `v0.1.0` in `pkg/mcpserver/server.go`. The endpoint uses
Streamable HTTP; no installable package is needed. Its optional, secret
`Authorization` header takes the complete `Bearer <token>` value described
in the [README](../README.md#token). Public ideas and repo venues can be read
without it; submitting ideas, listing your ideas, and reading your private
ideas require authentication.

## Publishing (automated)

The [`MCP Registry` workflow](../.github/workflows/mcp-registry-publish.yml)
publishes `server.json` on every **published GitHub release** and on manual
`workflow_dispatch`. Merging to `main` never publishes. The workflow:

1. Refuses to run unless `https://dibs.hivecommons.dev/readyz` reports the
   same sha as the checked-out ref (so a listing never points clients at a
   stale build — see [`deploy/README.md`](../deploy/README.md) to roll out).
2. Installs a pinned, checksum-verified `mcp-publisher`, runs
   `mcp-publisher validate server.json`, then
   `mcp-publisher login dns --domain hivecommons.dev` using the
   `MCP_REGISTRY_PRIVATE_KEY` repository secret, and `mcp-publisher publish`.
3. Reads the entry back by name and version (best effort; a slow registry
   does not fail the run).

To publish a new listing version: bump `version` in `server.json` (and the
MCP `serverVersion` in `pkg/mcpserver/server.go` if it changed), merge, roll
out, then publish a GitHub release or dispatch the workflow:

```sh
gh workflow run mcp-registry-publish.yml --repo hivecommons/dibs
```

### DNS authentication key

Authentication is DNS-based: the apex TXT record on `hivecommons.dev`
(`v=MCPv1; k=ed25519; p=<public key>`) must match the Ed25519 private key
(64 hex chars, as produced by the registry's
[DNS authentication instructions](https://github.com/modelcontextprotocol/registry/blob/main/docs/modelcontextprotocol-io/authentication.mdx#dns-authentication))
stored in the `MCP_REGISTRY_PRIVATE_KEY` secret. To rotate, generate a new
key pair, replace the TXT record (remove the old one — a stale record is tried
first and breaks verification), then update the secret.

## Operator checklist (manual fallback)

Use this only if the workflow is unavailable.

1. Confirm production runs current `main`. The listing sends clients straight
   to the endpoint, and the published image only reaches it after a rollout
   (see [`deploy/README.md`](../deploy/README.md)). From an up-to-date
   checkout, the version `/healthz` and `/readyz` report must match `main`,
   and `/readyz` must answer 200:

   ```sh
   git fetch origin main
   test "$(curl --fail --silent --show-error https://dibs.hivecommons.dev/readyz | jq -r .version)" \
     = "$(git rev-parse origin/main)" && echo "production is current"
   ```

   If the request fails or the versions differ, roll out the current image
   first and repeat this check. Do not publish against an older build.

2. Install the official `mcp-publisher` CLI using the
   [registry quickstart](https://github.com/modelcontextprotocol/registry/blob/main/docs/modelcontextprotocol-io/quickstart.mdx#step-3-install-mcp-publisher).
   From the repository root, validate the committed manifest:

   ```sh
   mcp-publisher validate server.json
   ```

3. Follow the registry's
   [DNS authentication instructions](https://github.com/modelcontextprotocol/registry/blob/main/docs/modelcontextprotocol-io/authentication.mdx#dns-authentication)
   for **hivecommons.dev**. The operator generates and retains the signing
   key, adds the public-key TXT record at the domain apex, and waits for DNS
   propagation. Keep the private key outside this checkout. With the private
   key in the format required by those instructions:

   ```sh
   mcp-publisher login dns --domain hivecommons.dev --private-key "$MCP_REGISTRY_PRIVATE_KEY"
   ```

4. From the repository root, publish the reviewed manifest:

   ```sh
   mcp-publisher publish server.json
   ```

5. Verify the published name, version, and remote URL through the registry:

   ```sh
   curl --fail --silent --show-error \
     'https://registry.modelcontextprotocol.io/v0.1/servers?search=dev.hivecommons%2Fdibs' \
     | jq -e '.servers[]
       | select(.server.name == "dev.hivecommons/dibs" and .server.version == "0.1.0")
       | select(any(.server.remotes[];
           .type == "streamable-http" and .url == "https://dibs.hivecommons.dev/mcp"))'
   ```

   Record the successful verification in #375 before closing it. When the
   listing changes, update its version and this verification command, and
   repeat step 1, before publishing again.

See also the registry's
[remote server guidance](https://github.com/modelcontextprotocol/registry/blob/main/docs/modelcontextprotocol-io/remote-servers.mdx).
