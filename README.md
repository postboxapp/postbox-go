# Postbox Go SDK

Official Go client for [Postbox](https://postboxapp.cloud), mailbox
infrastructure for developers. Provision real mailboxes over REST, send and
receive mail, stream events, and verify webhooks. One client covers both the
data plane and the management plane. Standard library only, no dependencies.

## Install

```bash
go get github.com/postboxapp/postbox-go@latest
```

Requires Go 1.22+.

## Quickstart

```go
package main

import (
	"context"
	"log"

	postbox "github.com/postboxapp/postbox-go"
)

func main() {
	pb, err := postbox.NewWithKey("pb_live_...")
	if err != nil {
		log.Fatal(err)
	}

	res, err := pb.Messages.Send(context.Background(), &postbox.MessagesSendBody{
		From:     "support@acme.com",
		To:       []map[string]any{{"address": "jess@northwind.io"}},
		Subject:  "Welcome to Acme",
		BodyHtml: "<p>Glad you are here.</p>",
	})
	if err != nil {
		log.Fatal(err)
	}
	log.Println(res.MessageId)
}
```

For a project-scoped key, custom base URL, timeout, or retries:

```go
pb, err := postbox.New(postbox.Options{
	APIKey:     os.Getenv("PB_KEY"),
	ProjectID:  os.Getenv("PROJECT_ID"), // sets X-Project-Id on every request
	TimeoutMs:  30000,
	MaxRetries: 2,
})
```

## What you get

- One client, both planes: `pb.Messages.Send(...)` and `pb.Domains.Create(...)`.
- Typed errors: failures are a `*postbox.Error` with `Status`, `Code`, and
  `RequestID`; use `postbox.IsRateLimit(err)`, `IsNotFound`, `IsValidation`, `IsAuth`.
- Idempotent sends: an `Idempotency-Key` is attached to writes and reused across
  retries, so a retried send never duplicates.
- Real-time events over SSE with automatic reconnect:

  ```go
  pb.Stream(ctx, func(e postbox.Event) error {
  	log.Println(e.Event)
  	return nil
  })
  ```

- Webhook signature verification:
  `postbox.VerifyWebhook(payload, signatureHeader, secret)`.

Retries, idempotency, event streaming, and webhook verification are built in and
behave consistently across every Postbox SDK.
