# Eval: slowmessage (base SmolLM3-3B)

- Repo: `hashicorp/terraform` `internal/helper/slowmessage/slowmessage.go`
- Ground-truth `go test`: **PASS**
- Generated `go test`: **FAIL** (build failed)
- Generation: 372 tokens in 35.0s on Apple Silicon MPS
- Model: `HuggingFaceTB/SmolLM3-3B`, `/no_think`, temperature 0.2

## Extracted Go

See `slowmessage_extracted.go`. Compiler errors:

```
./slowmessage_test.go:21:10: undefined: Do
./slowmessage_test.go:30:10: undefined: Do
```

The model chose `package slowmessage_test` (external test package) and then called `Do` without importing the package under test. The dataset's ground truth uses `package slowmessage` — same package as the source file.

## Raw model output

The model wrapped the file in a markdown fence, then added a paragraph of explanation after the closing fence. The extractor must take the fenced block, not the English.
