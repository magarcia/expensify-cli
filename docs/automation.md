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

Dry runs validate local input and return the command parameters without authenticating or calling Expensify. They can't verify report state, ownership, workspace membership, permissions, or server behavior. Remove `--dry-run` to execute. Deletion, report submission, and report approval also require `--yes` when input isn't interactive:

```sh
expensify report submit --report-id 456 --dry-run --no-input --output json
expensify report submit --report-id 456 --yes --no-input --output json
```

The HTTP client renews an expired session and retries once after Expensify's `407` response. It doesn't automatically retry transport or server failures: a write might already have succeeded. After an ambiguous failure, inspect the resource before issuing another write.

Use the CLI for all resource reads and writes. Browser automation is only for an explicitly requested login; don't fall back to browser writes when a resource command fails.

## Choose where an expense goes

`expense create` accepts a workspace, an existing draft report, or both:

| Target | Behavior |
| --- | --- |
| `--policy-id POLICY_ID` | Create through your own policy chat and a draft report in that workspace. |
| `--report-id REPORT_ID` | Add to your own existing draft and infer its workspace. |
| Both flags | Use that draft; its workspace must match `--policy-id`. |
| Neither flag | Use the configured `policy-id` default when present; otherwise track a personal expense. |

The workspace default can come from environment variables, project configuration, or user settings. Inspect effective settings with `expensify config list`. Use an explicit report ID when the expense must belong to a particular monthly draft.

Add `--receipt /private/path/receipt.pdf` to send a receipt with the original expense creation request. It accepts readable JPEG, PNG, or PDF files with matching extensions, up to 32 MiB. There isn't a second attach request to repeat if creation returns an ambiguous error; inspect the expense before retrying. Use `expense attach --expense-id ID --file FILE` for a receipt added to an existing expense.

`expense move --expense-id EXPENSE_ID --report-id REPORT_ID` moves your own expense to your own draft. An unreported source is allowed. A source already in a report must be in your own draft in the same workspace. The CLI rejects submitted reports, another owner's reports, and workspace mismatches before sending the mutation.

The CLI asks Expensify to defer immediate submission when creating a workspace expense. This doesn't change the workspace's scheduled submission rules; those can still submit the report later.

## Monthly draft workflow

Find the workspace and your existing draft before creating another report:

```sh
expensify workspace list --no-input --output llm
expensify report list --policy-id YOUR_POLICY_ID --status drafts \
  --no-input --output llm
```

If the monthly draft doesn't exist, create it:

```sh
expensify report create --policy-id YOUR_POLICY_ID --name 'October expenses' \
  --notes 'Health, Software License, Books, Courses & Internet' \
  --no-input --output llm
```

Before creating a workspace report, the CLI reads its policy and refuses the known instant-submit combination: `autoReporting=true`, `autoReportingFrequency=instant`, and an `approvalMode` other than `optional`. The report-creation API has no option to defer that immediate submission. Other workspace submission schedules still apply.

`--notes` sets the workspace's text report field named Notes. `report create --notes` requires `--policy-id`; the CLI checks that the workspace has one unambiguous text Notes field before creating the report. Creating the report and saving its Notes require separate requests.

If the Notes step returns an error after creation, the CLI reads the new report up to twice, waiting 250 ms before the second read, within a three-second verification budget. It returns success when the report's attached Notes value exactly matches the requested text. Workspace defaults don't count as confirmation. Neither write is replayed. If the value can't be confirmed, `API-006` preserves the original error reason and identifies the created report. Inspect it and use `report edit` with that ID if Notes are missing or incorrect. Don't create another report.

Update an existing report's Notes with `report edit`. Preview the change first:

```sh
expensify report edit --report-id 456 \
  --notes 'Health, Software License, Books, Courses & Internet' \
  --dry-run --no-input --output llm
```

Remove `--dry-run` to save the Notes. The report must be your own draft expense report with one text Notes field. Use `--notes=''` to clear optional Notes; required fields reject empty or whitespace-only text. Read `notes` from `report view` to check the result.

Use the selected or returned report ID below. Inspect its workspace, owner, and draft state, and list valid category and tag names:

```sh
expensify report view --report-id 456 --no-input --output llm
expensify category list --policy-id YOUR_POLICY_ID --no-input --output llm
expensify tag list --policy-id YOUR_POLICY_ID --no-input --output llm
expensify expense list --report-id 456 --no-input --output llm
```

Replace `456`, `Meals`, `Project`, and the receipt path with your actual values. Preview the expense locally:

```sh
expensify expense create --policy-id YOUR_POLICY_ID --report-id 456 \
  --amount 1250 --currency USD --merchant Cafe --date 2026-10-17 \
  --category Meals --tag Project --receipt /private/path/receipt.jpg \
  --dry-run --no-input --output llm
```

After reviewing the preview, send the same command without `--dry-run`:

```sh
expensify expense create --policy-id YOUR_POLICY_ID --report-id 456 \
  --amount 1250 --currency USD --merchant Cafe --date 2026-10-17 \
  --category Meals --tag Project --receipt /private/path/receipt.jpg \
  --no-input --output llm
expensify expense list --report-id 456 --no-input --output llm
expensify report view --report-id 456 --no-input --output llm
```

Read back the expense's report, amount, category, tag, and receipt metadata. This workflow doesn't call `report submit`. The server checks still happen during the live command; a successful dry run doesn't prove the draft is writable.

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

Workspace, category, and tag lists return a bare array without pagination metadata. Empty results are `[]`. For example, read workspace IDs with:

```sh
expensify workspace list --no-input --output json | jq -r '.[].id'
```

If a workspace's member picker endpoint rejects a category or tag request, the CLI reads the complete policy through Expensify's policy API. Both paths preserve disabled entries and tag group names.

Views return one object. Mutations return an affected resource ID and action, for example `{"id":"123","action":"created"}`. Expense mutations can also include `report_id` when the destination is known:

```json
{"id":"123","action":"created","report_id":"456"}
```

Treat `report_id` as optional; don't infer that an expense is personal just because a mutation result omits it. Read the expense or report back when placement matters. The exact resource fields come from the [typed models](https://github.com/magarcia/expensify-cli/blob/main/internal/expensify/model.go):

| Resource | Fields |
| --- | --- |
| Expense | `id`, `report_id`, `amount`, `currency`, `merchant`, `date`, `comment`, `category`, `tag`, `billable`, `reimbursable`, `receipt` |
| Report | `id`, `name`, `notes`, `policy_id`, `type`, `total`, `currency`, `state`, `status`, `owner_account_id`, `manager_id` |
| Workspace | `id`, `name`, `type`, `currency`, `role`, `owner` |
| Category or tag | `name`, `enabled`, `group` |

Optional fields can be absent. `receipt` preserves the server's receipt metadata. Report `state` and `status` preserve upstream values as strings. Consumers should tolerate additional fields.

In `report view`, `notes` is always a string, including `""` when the report's Notes are empty or absent. It contains the Notes report field's value or default text. Report lists omit this field because Search doesn't provide complete Notes metadata.

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
