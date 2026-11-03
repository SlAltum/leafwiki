# 语义检索的模型与推理库

这两样东西体积大且更新独立，不放在版本库里。协作者要让语义检索跑起来，
需要自己下载一次。

| 目录 | 内容 | 用途 |
|---|---|---|
| `assets/tokenizer/` | all-MiniLM-L6-v2（384 维，英文与多语通用），另含 ONNX Runtime 的 zip | 早期默认模型，测试与体积对照 |
| `assets/tokenizer-bge/` | bge-small-zh-v1.5（512 维，中文），`model.onnx` 约 91MB | 桌面版实际发布的模型 |

两者都被 `.gitignore` 忽略（`assets/tokenizer/`、`assets/tokenizer-*/`）。
不放仓库的原因很简单：单个模型接近 100MB，且模型更新与代码更新互不相干。

## 目录布局

每个模型目录必须自带 `manifest.json`，服务端靠它找到并校验模型。

```
assets/tokenizer-bge/
  manifest.json      描述文件（字段在下一节）
  model.onnx         导出的推理模型
  tokenizer.json     WordPiece 词表
  config.json        原始模型配置，仅作留档
```

`assets/tokenizer/` 里还存着推理库压缩包与解压结果：

```
assets/tokenizer/onnxruntime-win-x64-1.31.1.zip
assets/tokenizer/ort/onnxruntime-win-x64-1.31.1/lib/onnxruntime.dll
```

## 获取模型

两条路，任选一条。

**一、下载现成的 ONNX 导出。** HuggingFace 上有 transformers.js 格式的仓库
（例如 `Xenova/bge-small-zh-v1.5`、`Xenova/all-MiniLM-L6-v2`），取其中的
`onnx/model.onnx` 与 `tokenizer.json`。省事，但导出参数可能与仓库里的
`manifest.json` 不同，见下一节的校验。

**二、自己导出。** 需要 Python 环境：

```bash
pip install "optimum[onnxruntime]" transformers
optimum-cli export onnx --model BAAI/bge-small-zh-v1.5 --task feature-extraction out/
cp out/model.onnx out/tokenizer.json assets/tokenizer-bge/
```

换英文模型时把 `BAAI/bge-small-zh-v1.5` 换成 `sentence-transformers/all-MiniLM-L6-v2`，
目标目录换成 `assets/tokenizer/`。

## 获取 ONNX Runtime

推理库与 Go 绑定版本互相锁定：绑定的是 ONNX Runtime C API **1.31**，
其他版本会报 `Error setting ORT API base`。

从 [onnxruntime releases](https://github.com/microsoft/onnxruntime/releases) 下载
`onnxruntime-win-x64-1.31.1.zip`，解压后取 `lib/onnxruntime.dll`：

```bash
unzip onnxruntime-win-x64-1.31.1.zip -d assets/tokenizer/ort
```

桌面版发布时把 `onnxruntime.dll` 放在 exe 旁边，模型放在 `models/<id>/` 下。

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

| 字段 | 作用 |
|---|---|
| `dim` | 向量维度，必须与模型输出一致，写错会让检索结果全错 |
| `pooling` | `cls` 或 `mean`，与模型的训练方式对应；bge 系列用 `cls`，sentence-transformers 用 `mean` |
| `query_prefix` | 查询侧要加的指令前缀，bge 中文模型必须带；文档侧不加 |
| `max_tokens` | 单段最大 token 数，超出按段切分 |
| `files` | 每个文件的 sha256 与字节数，加载前逐个校验 |

## 校验

服务端在启动编码器之前会比对 `files` 里记录的 sha256，不一致就拒绝加载
（`ErrChecksumMismatch`）。所以换了模型的导出结果，必须同步 `manifest.json`：

```bash
sha256sum assets/tokenizer-bge/model.onnx assets/tokenizer-bge/tokenizer.json
```

把新的摘要与字节数写回 `files`，维度、池化方式、前缀按新模型调整。

自检三步：

1. 启动日志出现 `semantic search enabled mode=auto model=<id>`，并且没有
   `semantic search unavailable`
2. 管理员登录后 `GET /api/admin/search/vectors/status` 返回 `"state":"fresh"`
3. 发一条“改写”查询，结果里 `semantic` 为 `ok` 且能命中目标页

## 本地怎么用

测试通过环境变量指向本地目录，没有它们时相关用例会被跳过：

```bash
export LEAFWIKI_VECTORS_TEST_MODELS_DIR="$PWD/assets/tokenizer-bge"
export LEAFWIKI_VECTORS_TEST_LIBRARY="$PWD/assets/tokenizer/ort/onnxruntime-win-x64-1.31.1/lib/onnxruntime.dll"
CGO_ENABLED=1 go test -tags vectors ./internal/embed
```

运行期有两个参数：

- `--vectors-model-dir`：模型目录本身，或者只含一个模型的父目录；父目录下有两个以上
  模型时会报 `ErrModelAmbiguous`，这种情况直接指向具体目录
- `--vectors-library`：`onnxruntime.dll` 的路径

桌面版启动器会自动发现 exe 旁边的 `onnxruntime.dll` 与 `models\\`，不需要手动传。

## 缺失时的行为

- 缺模型或缺推理库不会让服务起不来：日志里有一条 `semantic search unavailable`，
  检索退回纯关键词。
- 不带 `vectors` 构建标签的服务端只接受 `--vectors=off`，其他取值启动即报错。
- 模型文件被改动或复制不完整时，校验失败并按上一条降级，不会产生错误向量。

## 许可

模型与推理库各自带许可，发布前请核对模型卡：
bge-small-zh-v1.5 为 MIT，all-MiniLM-L6-v2 为 Apache-2.0，ONNX Runtime 为 MIT。
