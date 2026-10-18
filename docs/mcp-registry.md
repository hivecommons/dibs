# Publishing Dibs to the MCP Registry

The root [`server.json`](../server.json) prepares the remote-only listing
`dev.hivecommons/dibs`, using the namespace approved in
[issue #375](https://github.com/hivecommons/dibs/issues/375#issuecomment-6215095421).
Committing this file does not publish it. Keep #375 open until the operator
has published and verified the entry.

The listing is titled **Dibs (Hive Commons)** to distinguish it from the
unrelated 1stDibs marketplace. Version `0.1.0` corresponds to the MCP
implementation's `v0.1.0` in `pkg/mcpserver/server.go`. The endpoint uses
Streamable HTTP; no installable package is needed. Its optional, secret
`Authorization` header takes the complete `Bearer <token>` value described
in the [README](../README.md#token). Public ideas and repo venues can be read
without it; submitting ideas, listing your ideas, and reading your private
ideas require authentication.

## Operator checklist

1. Install the official `mcp-publisher` CLI using the
   [registry quickstart](https://github.com/modelcontextprotocol/registry/blob/main/docs/modelcontextprotocol-io/quickstart.mdx#step-3-install-mcp-publisher).
   From the repository root, validate the committed manifest:

   ```sh
   mcp-publisher validate server.json
   ```

2. Follow the registry's
   [DNS authentication instructions](https://github.com/modelcontextprotocol/registry/blob/main/docs/modelcontextprotocol-io/authentication.mdx#dns-authentication)
   for **hivecommons.dev**. The operator generates and retains the signing
   key, adds the public-key TXT record at the domain apex, and waits for DNS
   propagation. Keep the private key outside this checkout. With the private
   key in the format required by those instructions:

   ```sh
   mcp-publisher login dns --domain hivecommons.dev --private-key "$MCP_REGISTRY_PRIVATE_KEY"
   ```

3. From the repository root, publish the reviewed manifest:

   ```sh
   mcp-publisher publish server.json
   ```

4. Verify the published name, version, and remote URL through the registry:

   ```sh
   curl --fail --silent --show-error \
     'https://registry.modelcontextprotocol.io/v0.1/servers?search=dev.hivecommons%2Fdibs' \
     | jq -e '.servers[]
       | select(.server.name == "dev.hivecommons/dibs" and .server.version == "0.1.0")
       | select(any(.server.remotes[];
           .type == "streamable-http" and .url == "https://dibs.hivecommons.dev/mcp"))'
   ```

   Record the successful verification in #375 before closing it. When the
   listing changes, update its version and this verification command before
   publishing again.

See also the registry's
[remote server guidance](https://github.com/modelcontextprotocol/registry/blob/main/docs/modelcontextprotocol-io/remote-servers.mdx).
