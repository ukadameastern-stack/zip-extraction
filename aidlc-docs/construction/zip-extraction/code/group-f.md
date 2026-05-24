# Group F Summary — Step 26 (PBT Generator Catalogue)

`services/zip-extraction/test/generators/generators.go` centralises pgregory.net/rapid generators for the domain types per PBT-07 (generator quality):

| Generator | Domain | Used by |
|---|---|---|
| `ClaimCheck()` | extraction.ClaimCheck (FR-1.2-shaped) | sqs.parseMessage tests, extraction-service tests |
| `ArchiveMetadata()` | valid bounded archive metadata | bombdefence.Checker.PreCheck tests |
| `ArchiveMetadataBomb(rule)` | metadata violating rules #1 or #4 | bombdefence negative-case tests |
| `EntryInfo()` | valid entry | bombdefence.EntryCheck tests |
| `EntryInfoBomb(rule)` | entry violating #5, #6, or #9 | bombdefence negative-case tests |
| `RawPath()` | legitimate paths | validation.Sanitize positive tests |
| `RawPathTraversal()` | adversarial `..` variants (including URL-encoded + backslash) | validation negative tests |
| `RawPathAbsolute()` | absolute Unix + Windows drive-letter paths | validation negative tests |
| `EntryOutcome(failureProb)` | mixed UPLOADED + FAILED outcomes | extraction.computeStatus + slipsheet.Build tests |
| `PipelineFile()` | DDB row generator | dynamodb.Marshal/Unmarshal round-trip + slipsheet round-trip |

All generators draw from `*rapid.T` and use bounded ranges where domain constraints exist (e.g., bucket-name regex, entry-count [0,5000]). PBT-07 compliant: domain-typed, parameterisable, centralised — tests do NOT define ad-hoc generators inline.
