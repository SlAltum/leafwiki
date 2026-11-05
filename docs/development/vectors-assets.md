# Semantic search assets (model and runtime library)

The model and the inference library are large and change on their own schedule, so they are not
in the repository. A contributor who wants semantic search running downloads them once.

| Directory | Content | Purpose |
|---|---|---|
| `assets/tokenizer/` | all-MiniLM-L6-v2 (384 dimensions, English and general multilingual), plus the ONNX Runtime zip | The early default model; kept for tests and size comparison |
| `assets/tokenizer-bge/` | bge-small-zh-v1.5 (512 dimensions, Chinese), `model.onnx` at about 91 MB | The model the desktop build ships with |

Both are ignored by `.gitignore` (`assets/tokenizer/`, `assets/tokenizer-*/`). The reason is
simple: one model is close to 100 MB, and a model update has nothing to do with a code update.

## Directory layout

Every model directory carries a `manifest.json`. The server finds and validates the model
through it.

```
assets/tokenizer-bge/
  manifest.json      description (fields in the next section)
  model.onnx         the exported model
  tokenizer.json     the WordPiece vocabulary
  config.json        the original model configuration, kept for reference
```

`assets/tokenizer/` also holds the runtime archive and its extraction:

```
assets/tokenizer/onnxruntime-win-x64-1.31.1.zip
assets/tokenizer/ort/onnxruntime-win-x64-1.31.1/lib/onnxruntime.dll
```

## Getting the model

Two ways, pick one.

**1. Download a ready ONNX export.** HuggingFace hosts transformers.js repositories (for example
`Xenova/bge-small-zh-v1.5` and `Xenova/all-MiniLM-L6-v2`); take `onnx/model.onnx` and
`tokenizer.json` from one. This is quicker, but the export settings may differ from the
`manifest.json` in the repository. See the validation section below.

**2. Export it yourself.** This needs a Python environment:

```bash
pip install "optimum[onnxruntime]" transformers
optimum-cli export onnx --model BAAI/bge-small-zh-v1.5 --task feature-extraction out/
cp out/model.onnx out/tokenizer.json assets/tokenizer-bge/
```

For an English model, replace `BAAI/bge-small-zh-v1.5` with
`sentence-transformers/all-MiniLM-L6-v2` and copy into `assets/tokenizer/`.

## Getting ONNX Runtime

The inference library and the Go binding are locked to each other: the binding uses ONNX Runtime
C API **1.31**, and another version fails with `Error setting ORT API base`.

Download `onnxruntime-win-x64-1.31.1.zip` from
[onnxruntime releases](https://github.com/microsoft/onnxruntime/releases), extract it, and take
`lib/onnxruntime.dll`:

```bash
unzip onnxruntime-win-x64-1.31.1.zip -d assets/tokenizer/ort
```

The desktop build ships `onnxruntime.dll` next to the exe and the model under `models/<id>/`.

## manifest.json

```json
{
  "model_id": "bge-small-zh-v1.5",
  "dim": 512,
  "dtype": "float32",
  "tokenizer": "wordpiece",
  "max_tokens": 512,
  "pooling": "cls",
  "query_prefix": "为这个句子生成表示以用于检索相关文章：",
  "files": [
    { "name": "model.onnx", "sha256": "69a0b846…", "bytes": 94851877 },
    { "name": "tokenizer.json", "sha256": "48cea5d4…", "bytes": 439125 }
  ]
}
```

| Field | What it does |
|---|---|
| `dim` | Vector dimension. It must match the model output; a wrong value makes every result wrong. |
| `pooling` | `cls` or `mean`, matching how the model was trained. bge models use `cls`, sentence-transformers uses `mean`. |
| `query_prefix` | Instruction prefix added to a query. The bge Chinese model needs it; the document side never gets it. |
| `max_tokens` | Maximum tokens per passage; longer text is split. |
| `files` | sha256 and byte count per file, checked before loading. |

## Validation

Before the encoder starts, the server compares the sha256 of every file against `files` and
refuses to load on a mismatch (`ErrChecksumMismatch`). A new export therefore needs a matching
`manifest.json`:

```bash
sha256sum assets/tokenizer-bge/model.onnx assets/tokenizer-bge/tokenizer.json
```

Write the new digests and byte counts back into `files`, then set dimension, pooling and prefix
for the new model.

Three checks:

1. The startup log shows `semantic search enabled mode=auto model=<id>` and no
   `semantic search unavailable`.
2. After an admin login, `GET /api/admin/search/vectors/status` returns `"state":"fresh"`.
3. A paraphrased query returns `semantic` as `ok` and finds the target page.

## Using it locally

The tests read two environment variables and are skipped without them:

```bash
export LEAFWIKI_VECTORS_TEST_MODELS_DIR="$PWD/assets/tokenizer-bge"
export LEAFWIKI_VECTORS_TEST_LIBRARY="$PWD/assets/tokenizer/ort/onnxruntime-win-x64-1.31.1/lib/onnxruntime.dll"
CGO_ENABLED=1 go test -tags vectors ./internal/embed
```

At runtime there are two flags:

- `--vectors-model-dir`: the model directory itself, or a parent that holds exactly one model. A
  parent with two or more models fails with `ErrModelAmbiguous`; point at the concrete directory
  in that case.
- `--vectors-library`: the path to `onnxruntime.dll` (or `libonnxruntime.so`).

The desktop launcher finds `onnxruntime.dll` and `models\` next to the exe by itself, so you do
not pass either flag there.

## Behavior when something is missing

- A missing model or missing library does not stop the server: the log has one
  `semantic search unavailable` line and search falls back to keywords.
- A server build without the `vectors` tag accepts only `--vectors=off`; any other value fails at
  startup.
- A modified or incompletely copied model file fails validation and degrades the same way. It
  never produces wrong vectors.

## License

The model and the runtime library carry their own licenses. Check the model card before
shipping: bge-small-zh-v1.5 is MIT, all-MiniLM-L6-v2 is Apache-2.0, ONNX Runtime is MIT.
