/*
Wasim is a local emulator of the WhatsApp Cloud API.

It serves a fake Graph API that a WhatsApp integration can point its base URL
at, delivers signed webhooks to the integration, and offers a browser console
where a developer plays the WhatsApp user. No Meta account, phone or tunnel is
involved.

Usage:

	wasim [flags]
	wasim user add|passwd|remove|list [EMAIL] [--password-stdin] [--db-path PATH]

Every flag has an environment variable, shown in brackets in `wasim --help`. A
flag beats its variable, which beats the default. The most common ones:

	--webhook-url URL      endpoint that receives webhooks (WASIM_WEBHOOK_URL)
	--app-secret SECRET    signs webhooks with X-Hub-Signature-256 (WASIM_APP_SECRET)
	--verify-token TOKEN   token for the verification handshake (WASIM_VERIFY_TOKEN)
	--graph-addr ADDR      fake Graph API, default 127.0.0.1:7070 (WASIM_GRAPH_ADDR)
	--console-addr ADDR    console, default 127.0.0.1:7071 (WASIM_CONSOLE_ADDR)
	--db-path PATH         SQLite file, or :memory: (WASIM_DB_PATH)
	--seed-file PATH       templates and contacts to start with (WASIM_SEED_FILE)
	--version              print the version and exit

Example:

	wasim --webhook-url http://localhost:3000/webhook --app-secret local-app-secret

Then point the integration's Graph API base URL at http://localhost:7070 and
open the console at http://localhost:7071.

The console is open to anyone who reaches it until a user exists. Add one to
require a login, from the same database the server uses:

	wasim user add ana@example.com

Scripts that drive the console API then send the same credentials with HTTP
Basic authentication, as in curl -u.
*/
package main
