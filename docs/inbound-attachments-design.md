# Inbound attachment batching

Status: V1 implemented on the `custom/inbox-attachments` branch.

## Goal

Accept non-text WeChat messages without dispatching each item immediately. Store
them in a per-conversation inbox and attach the pending batch to the next text
message sent to the agent.

## User flow

1. A user sends one or more images, files, videos, or voice messages.
2. WeClaw downloads and decrypts every supported item into that conversation's
   inbox. The agent is not called yet.
3. The next text message is treated as the instruction for the pending batch.
4. WeClaw atomically claims the batch and dispatches the text and attachments as
   one agent turn.
5. A successful dispatch marks the batch consumed. A failed dispatch returns the
   batch to pending so that it can be retried.

Files arriving after a batch has been claimed belong to the next batch.

## Storage model

Use a hash of the conversation ID in directory names so that raw WeChat IDs are
not exposed on disk:

```text
~/.weclaw/inbox/<conversation-hash>/<batch-id>/
  manifest.json
  <sanitized-original-name>
```

The manifest records the WeChat message ID, original filename, local path, media
kind, detected MIME type, byte length, SHA-256, receive time, download state, and
batch state. Writes use a `.part` file followed by an atomic rename.

Batch states are `pending`, `claimed`, `consumed`, and `expired`. Attachment
states are `downloading`, `ready`, and `failed`.

## Ordering and concurrency

- Queues are isolated by conversation ID.
- A text message creates an ordering barrier. It waits for all earlier attachment
  downloads in that conversation to reach `ready` or `failed`.
- Claiming a batch is atomic. Attachments received after the claim are never
  added to an in-flight agent turn.
- Existing `message_id` deduplication applies before download and queue insertion.
- On restart, manifests are loaded so pending batches survive process crashes.

## Limits

Defaults are configurable:

- 10 attachments per batch.
- 100 MiB per attachment.
- 200 MiB total per batch.
- 30-minute pending window.

When a limit is reached, already accepted attachments remain pending and the new
item is rejected with an explicit WeChat reply. Expired files remain on disk for
retention/cleanup but are not silently attached to a later unrelated message.

## Agent input

Replace the text-only call internally with a structured input:

```go
type Input struct {
	Text        string
	Attachments []Attachment
}

type Attachment struct {
	Name      string
	Path      string
	MIMEType  string
	Size      int64
	SHA256    string
	Kind      string
}
```

For the Codex app-server adapter, images use `localImage` and audio uses
`localAudio`. Other files remain first-class WeClaw attachments and are exposed
to Codex through their absolute local paths plus metadata in the text input.
Adapters that do not support structured media receive the same path-and-metadata
fallback.

Attachments are untrusted user input. The generated instruction must explicitly
state that they may be read but must not be executed.

## Commands and feedback

- `/files`: list the current conversation's pending attachments.
- `/clear-files`: expire the pending batch without dispatching it.
- Successful queueing is logged as `queued attachment`, not `skipping`.
- Download, decryption, size, and format failures are reported to the user.
- A text-only message with no pending batch retains current behavior.

## Planned source changes

- `messaging/handler.go`: parse every item, queue media, and claim pending files
  when text arrives.
- `messaging/inbound_media.go`: shared download/decrypt/type-detection pipeline.
- `messaging/inbox.go`: persistent per-conversation batch state machine.
- `agent/agent.go`: structured input contract.
- `agent/acp_agent.go`: Codex `localImage` and `localAudio` inputs.
- CLI and HTTP adapters: safe path-and-metadata compatibility fallback.
- Unit and integration tests for ordering, limits, restarts, duplicates, mixed
  messages, failed downloads, and concurrent arrival during dispatch.
