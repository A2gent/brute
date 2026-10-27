# Website credentials

Open **Settings > Tools & integrations > Secrets** in local Caesar to add a website URL, login, and password. Credentials are shared across projects in this Brute installation. They can be edited or deleted; leaving the password empty while editing preserves the saved password.

## Agent access

The user chose to give agents the actual password, rather than browser-only autofill. The built-in `website_credentials` tool supports:

- `{"operation":"list"}`: URL, login, ID, and timestamps, without passwords.
- `{"operation":"get","id":"<saved ID>"}`: the selected URL, login, and raw password.

Existing tool enable/disable and agent tool filters apply. Direct host agents access the parent's vault through a session-bound, revocable capability, without sharing its data directory.

Passwords returned by the tool enter model/provider context and may persist in session history. The tool instructs agents to use a credential only for its saved site and not repeat passwords in answers. These instructions are not an enforced destination restriction. Give this tool only to trusted agents and use limited-permission website accounts.

## Storage and backups

All record fields are encrypted with AES-256-GCM under `<DataPath>/website-credentials/records.enc`. A random local key is kept in the adjacent `key` file. On POSIX, the directory is mode `0700` and key/data files are mode `0600`. Writes use a cross-process lock, atomic replacement, and directory syncing. Missing keys or corrupted ciphertext fail closed rather than generating a replacement key over existing records.

Back up the **entire website-credentials directory**, including its key, as sensitive data. Losing the key makes saved records unrecoverable. Someone who can read both the key and ciphertext can decrypt them; this is not an OS-keychain or hardware-backed vault.

Windows builds support locking, but owner-only ACL enforcement and power-loss durability need platform-specific hardening. Do not rely on POSIX file modes to protect a shared Windows data directory.

## HTTP boundary

`GET/POST /secrets` and `PUT/DELETE /secrets/{id}` return metadata only, never passwords. POST/PUT accept JSON; request bodies are size-limited and responses are not cacheable. Passwords are never placed in general settings or environment variables by this feature.

Vault management accepts only loopback socket peers, a loopback Host, and approved local Caesar/Tauri browser origins (or an explicitly configured exact CORS origin). Forwarded headers do not confer access. Remote/Square-proxied Caesar cannot manage this vault. Requests without a browser Origin are permitted from loopback.

These checks are defense in depth for the new routes, not authentication for the whole application. Existing Brute control/session endpoints remain a trusted control surface. Do not expose the server to untrusted networks; owner authentication is tracked separately as T-252.
