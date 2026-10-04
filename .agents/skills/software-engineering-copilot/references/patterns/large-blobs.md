# Handling Large Blobs — Condensed Design Playbook

Use this playbook for large photos, videos, archives, datasets, or documents whose bytes should not flow through ordinary API servers. A useful initial trigger is files around 10 MB or larger, then adjust for traffic, infrastructure, validation, and latency requirements.

## Core principle

Keep structured metadata in a database and bytes in object storage. The application authorizes and coordinates transfers; clients upload/download directly to storage or a CDN.

```text
1. client → API: request upload
2. API: authorize, check quota, create PENDING metadata row
3. API → client: scoped, short-lived signed upload URL/session
4. client → object storage: bytes/chunks
5. storage → event bus/worker: object-created/completed event
6. worker: verify/scan/process, update metadata to AVAILABLE
7. client → API: request download authorization
8. client → CDN/storage: bytes via signed URL/cookie
```

API servers never proxy the normal data plane.

## When direct transfer fits

Use direct storage access when:

- File size or aggregate bandwidth would bottleneck application servers.
- Transfers must resume after network interruption.
- Storage/CDN geographic capacity is better than the API fleet.
- Validation and processing can happen after upload.

Proxy or stream through a controlled service when bytes must be inspected before acceptance, regulations require a certified path, transformations must happen inline, or the immediate response depends on file contents. Even then, stream in chunks; do not buffer the whole object in memory.

## Upload initiation

The API should:

- Authenticate and authorize the user/tenant.
- Enforce quota, expected size, media type, and object-count limits.
- Generate the object key itself; never trust a client-chosen arbitrary path.
- Create metadata with `PENDING` status before issuing credentials.
- Bind a short-lived signed URL/policy to one operation, bucket, object key, size range, and allowed content type/checksum where supported.
- Return `file_id`, upload session details, expiry, chunk policy, and completion/status endpoints.

A safe opaque key resembles `uploads/{tenant_id}/{uuid}`. Store the user-visible filename as metadata, not as an authority-bearing path.

Anyone holding a signed URL can use it until expiry. Keep lifetime short and avoid logging the full URL.

## Multipart/resumable upload

Use for large or unreliable transfers:

1. Initiate a storage upload session and persist its ID against `file_id`.
2. Divide the file into bounded parts.
3. Upload parts independently, optionally with limited parallelism.
4. Record part number, size, and checksum/ETag client-side or in coordination state.
5. On reconnect, query uploaded parts and send only missing/failed ones.
6. Complete the session with the ordered part/checksum list.
7. Treat the object as unavailable until storage confirms completion.

Tune part size to balance retry cost, request overhead, provider limits, and client memory. Limit concurrency to avoid saturating mobile networks or browsers.

Apply storage lifecycle rules that abort and remove incomplete multipart sessions after roughly 24–48 hours or the product’s chosen recovery window.

## Metadata state machine

```text
PENDING → UPLOADING → UPLOADED → SCANNING/PROCESSING → AVAILABLE
    ↘ EXPIRED       ↘ FAILED            ↘ QUARANTINED/REJECTED
```

Suggested metadata:

- `file_id`, tenant/user, generated storage key, original filename.
- Declared and observed size/type/checksum.
- Upload/session ID, status, processing version, timestamps.
- Security scan/moderation result and derived-object references.
- Retention, visibility, legal hold, and deletion state where relevant.

Do not mark a file available merely because the client says upload completed.

## Synchronize object storage and metadata

- Use object-storage completion events as the primary confirmation.
- Make event handlers idempotent; delivery may be duplicated or delayed.
- Verify object key, size, checksum, tenant binding, and expected status before transition.
- Run a reconciliation job for old `PENDING`/`UPLOADED` rows and orphan objects.
- Define both directions: expire metadata for missing objects and quarantine/delete objects with no valid metadata record.
- Keep a stable `file_id`/storage-key correlation so events update the correct row.

Object storage and the database are separate systems; aim for convergent state, not a fictitious cross-system transaction.

## Security and abuse controls

- Upload first into a private quarantine prefix/bucket.
- Validate magic bytes, not only filename or client-provided MIME type.
- Enforce signed size/type/checksum conditions and account quotas.
- Scan for malware, content-policy violations, decompression bombs, and unexpected formats.
- Strip or sanitize unsafe metadata when appropriate.
- Promote/copy or change metadata to an `AVAILABLE` location/state only after checks pass.
- Keep buckets private; authorize downloads through signed URLs/cookies or an authenticated edge.
- Use tenant-scoped keys, encryption, audit logs, rate limits, and retention/deletion policies.
- Treat archive extraction paths and user filenames as untrusted.

## Download path

Choose:

- **Direct object-storage signed URL:** simple and economical for infrequent/private downloads.
- **CDN signed URL/cookie:** frequently reused or globally accessed objects; lower origin load and latency.

Support HTTP range requests so interrupted large downloads can resume. Ensure the CDN cache key and signature policy permit required range headers/methods.

For mutable files, use immutable versioned object keys so CDN caches never confuse versions. Set `Content-Disposition`, media type, cache policy, and filename safely at serving time.

## Processing pipeline

Storage completion can trigger asynchronous work such as transcoding, thumbnails, OCR, indexing, or moderation.

- Queue references (`file_id`, object version), not the bytes.
- Make each processing step idempotent and version derived outputs.
- Keep original uploads immutable where reprocessing/audit is required.
- Expose processing status and notify the user when the object becomes available.
- Use a workflow engine when processing has several dependent/compensating steps; use a queue for one independent task.

## Failure scenarios

| Failure | Handling |
|---|---|
| Upload dies at 99% | Query completed parts and resume only missing chunks |
| Client uploads but never calls API | Storage event updates metadata; reconciliation catches missed events |
| Metadata exists but no object | Expire/cancel after deadline; abort multipart session |
| Object exists without metadata | Quarantine and later delete/reconcile |
| Duplicate completion event | Idempotent conditional state transition |
| Bad or malicious file | Keep private in quarantine; reject before promotion |
| Slow/global download | CDN + range requests + immutable versions |
| Signed URL leaks | Short expiry, narrow scope, private origin; revoke/rotate object key when necessary |

## Avoid these mistakes

- Storing large binaries in relational rows without a specific reason.
- Proxying gigabytes through general API servers.
- Letting clients choose object keys or unrestricted upload properties.
- Trusting a client “complete” callback as proof the object is valid.
- Making uploaded bytes public before scanning/validation.
- Forgetting abandoned multipart storage costs and cleanup.
- Keeping rich searchable metadata only as object tags.
- Assuming CDN invalidation is immediate; prefer immutable versioned keys.
- Passing bytes through a queue or workflow history.

## Design review checklist

- [ ] File size, throughput, geography, resume, and validation requirements are quantified.
- [ ] Metadata and bytes have separate authoritative stores and stable correlation.
- [ ] Signed credentials are short-lived, single-purpose, and size/type/checksum constrained.
- [ ] Multipart resume, completion, expiry, and cleanup are defined.
- [ ] Storage events plus reconciliation converge metadata/object state.
- [ ] Quarantine, scanning, quota, tenant isolation, and private access are enforced.
- [ ] Downloads use the right direct/CDN path and support ranges.
- [ ] Processing steps are async, idempotent, observable, and versioned.
- [ ] Retention, deletion, orphan cleanup, and cost monitoring are covered.
