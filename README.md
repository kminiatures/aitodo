# aitodo

aitodo は AI エージェント向けの TODO ツールです。**セッション → タスク（→ サブタスク）** の階層で、AI がタスクを読み、上から順に消し込んでいきます。
各タスクには AI や人がコメントを付けて、作業の経過や結果を残せます。

- 1 つのバイナリで **TUI（マウス対応）**、**CLI（API 向けコマンド群）**、**MCP サーバー** の 3 つを兼ねます
- バックエンドは SQLite（WAL モード）です。AI が書き込んでいる最中でも TUI は 1.5 秒ごとに自動更新されるので、進捗をそのまま眺められます
- セッションに **作業フォルダ** を設定できます。そのフォルダ（サブフォルダも含む）でコマンドを実行すると、対象セッションが自動で選ばれます

## インストール

```sh
go install github.com/kminiatures/aitodo@latest   # ~/go/bin に入る（PATH に追加が必要）
# またはソースから
git clone https://github.com/kminiatures/aitodo && cd aitodo
make install          # ~/.local/bin/aitodo に入る（PREFIX=/usr/local などで変更可）
```

cgo は不要です（純 Go の SQLite ドライバ `modernc.org/sqlite` を使用）。

DB の場所は `~/.aitodo/aitodo.db` です。`AITODO_DB` 環境変数か `--db PATH` で変更できます。

## 使い方（人間）

```sh
cd ~/work/myproj
aitodo session add myproj --dir . --desc "認証まわりのリファクタ"
aitodo add "既存テストを通す"
aitodo              # TUI 起動
```

### TUI

```
┌─ Sessions ─────────┐┌─ myproj  1/3  ~/work/myproj ─────────────┐
│ myproj         1/3 ││ [x] #1 既存テストを通す                   │
│ other          0/2 ││ [>] #2 認証ミドルウェアを分離             │
│                    ││ [ ] #3 ドキュメント更新                   │
└────────────────────┘└──────────────────────────────────────────┘
┌─ Task #2  doing ─────────────────────────────────────────────────┐
│ 認証ミドルウェアを分離                                            │
│ → メモ / 結果                                                     │
└──────────────────────────────────────────────────────────────────┘
 +Session  +Task  ✓Done  ▶Start  !Block  ↑  ↓  Edit  Dir  Delete  …
```

| 操作 | マウス | キー |
|---|---|---|
| 選択 | クリック | `↑↓` / `j k`、`tab` で枠を切り替え |
| 完了をトグル | `[ ]` をクリック | `space` / `x` |
| サブタスク追加 | +Sub | `A` |
| コメント追加 | Comment | `c` |
| 詳細ビュー（コメント全文） | View | `v`（`esc` で戻る） |
| 進行中 / ブロック / スキップ | 下部ボタン | `s` / `b` / `-` |
| 編集 | ダブルクリック / Edit | `e` / `enter` |
| タスク追加 / セッション作成 | +Task / +Session | `a` / `n` |
| 作業フォルダ設定 | Dir | `w` |
| 並べ替え | ↑ ↓ ボタン | `K` / `J` |
| 削除 | Delete | `d`（確認あり） |
| アーカイブ / アーカイブも表示 | Archive | `z` / `H` |
| 完了タスクを隠す | HideDone | `f` |
| スクロール | ホイール | `PgUp` / `PgDn` |
| 詳細枠の高さ変更（次回起動時も維持） | 詳細枠の上辺をドラッグ | — |
| 終了 | Quit | `q` |

作業フォルダ欄では `tab` で bash のように補完します。候補が 1 つなら確定し、複数なら共通部分まで伸ばし、それ以上伸ばせないときは候補を一覧表示します（候補はクリックでも選べます）。`~` や相対パスも使えます。

フォームの操作: `tab`（作業フォルダ欄では `shift+tab`）/ `↑↓` で項目を移動、`enter` で次の項目へ（最後の項目なら保存）、`ctrl+s` で保存、`esc` でキャンセル。複数行のテキストは `\n` と入力します。

## 使い方（AI / スクリプト）

```sh
aitodo where --json                 # cwd からセッションを解決する
aitodo import <<'EOF'               # 計画を一括登録（1 行 1 タスク）
- 原因を調査
- 修正
    auth.go の redirect 処理            ← インデントした地の文は本文
    - middleware を直す                 ← インデントした箇条書きはサブタスク
    - 回帰テスト
- テスト追加
EOF
aitodo next --claim --json          # 次のタスクを取り doing にする（子 → 親の順。過去のコメントも返る）
aitodo comment 12 "途中経過や気づき"   # コメント（追記型のログ）
aitodo done 12 --note "一行要約" --comment "詳しい結果"   # 消し込む
aitodo sub 12 "追加で見つけた作業"     # サブタスクを追加
```

- サブタスクは何段でもネストできます。`next` は未完了の子を持たないタスクだけを返すので、子を片付けたあと最後に親が返ってきます
- `note` は結果の一行要約（上書き）で、コメントは何件でも積み上げられる追記型のログです。投稿者は `--author` か `$AITODO_AUTHOR` で指定し、既定は `ai` です。TUI から書くと `$USER` になります
- 複数のエージェントが同じセッションを並列に処理する場合は `aitodo next --claim --fresh` を使ってください（全員に別々のタスクが割り当たります）
- すべてのコマンドで `--json` が使えます。エラー時は終了コードが非 0 になり、stderr に `{"error": "..."}` を出します
- セッションを指定しない場合は `$AITODO_SESSION`、次にカレントディレクトリの順で解決します（作業フォルダの最長一致）
- マニュアル全文は `aitodo manual` で表示します（[docs/manual.md](docs/manual.md)）
- CLAUDE.md / AGENTS.md に貼る用のスニペットは `aitodo manual snippet >> CLAUDE.md` で追記できます

## MCP

```sh
claude mcp add aitodo -- aitodo mcp             # このプロジェクト用
claude mcp add -s user aitodo -- aitodo mcp     # 全プロジェクト共通
```

ツール: `session_list` `session_create` `session_get` `session_update` `task_add` `task_add_bulk` `task_list` `task_get` `task_next` `task_start` `task_done` `task_set_status` `task_comment` `task_comments` `task_edit` `task_move` `task_delete`

`session` を省略した場合、セッションは `workdir` 引数から解決し、それもなければ MCP サーバーの cwd（通常はプロジェクトルート）から解決します。

## 構成

```
main.go                 エントリポイント
internal/store          SQLite スキーマ・操作（CLI / MCP / TUI で共有）
internal/cli            コマンド解析・出力
internal/mcp            MCP サーバー（stdio, JSON-RPC 2.0）
internal/tui            bubbletea による TUI
docs/manual.md          AI 向けマニュアル（バイナリに埋め込み）
```
