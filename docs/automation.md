# Automation

Use explicit formats and disable prompts in scripts:

```sh
expensify expense list --limit 100 --offset 0 --no-input --output json
```

Successful data goes to stdout. Errors and progress go to stderr. `--quiet` suppresses progress while retaining command data. `--output auto` produces terminal tables on a TTY and plain tab-separated output when piped; scripts should select `--output json`.

## Write deliberately

Validate a mutation first:

```sh
expensify expense create \
  --amount 1250 --currency EUR --merchant Cafe --date 2026-10-17 \
  --dry-run --no-input --output json
```

Dry runs validate input and return the command parameters without authenticating or calling Expensify. Remove `--dry-run` to execute. Deletion, report submission, and report approval also require `--yes` when input isn't interactive:

```sh
expensify report submit --report-id 456 --dry-run --no-input --output json
expensify report submit --report-id 456 --yes --no-input --output json
```

The HTTP client renews an expired session and retries once after Expensify's `407` response. It doesn't automatically retry transport or server failures: a write might already have succeeded. After an ambiguous failure, inspect the resource before issuing another write.

## JSON contract

Resource JSON uses `snake_case` field names and string IDs. Treat IDs as opaque strings; don't convert them to JavaScript numbers. Amounts and report totals are integers in currency minor units. Expense amounts are positive for spending and negative for credits.

Expense and report lists return a page:

```json
{
  "items": [
    {
      "id": "123",
      "report_id": "456",
      "amount": 1250,
      "currency": "EUR",
      "merchant": "Cafe",
      "date": "2026-10-17",
      "billable": false,
      "reimbursable": true
    }
  ],
  "offset": 0,
  "limit": 100,
  "has_more": false
}
```

`items` is always an array, including for empty results. Increase `--offset` by the returned `limit` while `has_more` is true. The default limit is `100`; supported values are `1` through `1000`.

Views return one object. Mutations return `{"id":"123","action":"created"}` with the affected resource ID and action. The exact resource fields come from the [typed models](https://github.com/magarcia/expensify-cli/blob/main/internal/expensify/model.go):

| Resource | Fields |
| --- | --- |
| Expense | `id`, `report_id`, `amount`, `currency`, `merchant`, `date`, `comment`, `category`, `tag`, `billable`, `reimbursable`, `receipt` |
| Report | `id`, `name`, `policy_id`, `type`, `total`, `currency`, `state`, `status`, `owner_account_id`, `manager_id` |
| Workspace | `id`, `name`, `type`, `currency`, `role`, `owner` |
| Category or tag | `name`, `enabled`, `group` |

Optional fields can be absent. `receipt` preserves the server's receipt metadata. Report `state` and `status` preserve upstream values as strings. Consumers should tolerate additional fields.

For example, extract expense IDs with `jq`:

```sh
expensify expense list --no-input --output json | jq -r '.items[].id'
```

## Errors and exit codes

With `--output json`, a failure writes a structured object to stderr and exits nonzero:

```json
{
  "error": {
    "code": "AUTH-001",
    "message": "No Expensify credentials are available.",
    "hint": "Run expensify auth login, or set the EXPENSIFY authentication environment variables."
  }
}
```

Check the exit code before parsing stdout. Error codes identify the failure; messages and hints explain how to recover.

| Exit | Meaning |
| --- | --- |
| `0` | Success |
| `1` | General, configuration, or upstream API failure, including HTTP 5xx |
| `2` | Invalid usage, invalid input, or missing confirmation |
| `3` | Authentication failure |
| `4` | Missing or inaccessible resource |
| `5` | Permission denied |
| `10` | Network failure, timeout, or rate limiting |
| `130` | Interrupted with SIGINT |
| `143` | Terminated with SIGTERM |

Broken pipes exit quietly. Run `expensify help exit-codes` for the installed version's mapping.

## Isolate credentials and defaults

Use credential imports or environment variables for unattended authentication. They don't open a browser. `auth login --no-input` fails if no credentials are available, without prompting or opening a browser.

Use a private configuration directory for each automation identity:

```sh
expensify --config-dir /private/path/expensify-bot auth login \
  --credentials-stdin --no-input < /private/path/device.json
expensify --config-dir /private/path/expensify-bot report list \
  --no-input --output json
```

The configuration directory identifies a separate OS keyring entry. The directory must meet the platform's private-storage permissions described in [authentication](authentication.md). You can set `EXPENSIFY_CONFIG_DIR` for the process instead.

`EXPENSIFY_CREDENTIAL_STORE=auto` prefers the OS keyring and falls back to a private plaintext file when unavailable, as can happen on headless hosts. Set it to `keyring` to fail instead of using plaintext, or `file` to select the private file explicitly. `auth login` and `auth status` report the selected `storage`; `path` identifies a file only for file storage.

Environment credentials override stored credentials. Resource commands keep these credentials and any renewed token in memory. Only an explicit `auth login` persists supplied credentials. For CI, injecting credentials from a secret store avoids local persistence.

For an initial login with a person present, explicitly request a browser:

```sh
expensify auth login --browser chrome --login-timeout 10m --no-input --output json
```

The person signs in using the temporary browser window. With `--output json`, login suppresses progress messages and returns only the result on stdout or a structured error on stderr. When login ends, the CLI closes that browser and removes its temporary profile. See [authentication](authentication.md) for supported browsers and manual alternatives.

For shared, nonsecret defaults, `.expensify.json` in the current working directory accepts `output`, `policyID`, `currency`, and `timeout`:

```json
{
  "output": "json",
  "policyID": "YOUR_POLICY_ID",
  "currency": "EUR",
  "timeout": "30s"
}
```

Don't put tokens or device secrets in project configuration. Flags override environment variables, project settings, user settings, and built-in defaults, in that order.
