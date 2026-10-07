---
name: devlog
description: |
  動作確認で撮ったスクリーンショット・動画と、その作業の概要を、プロジェクトの devlog/ に
  開発履歴として記録し、ローカルで見られる HTML（devlog/index.html）を作り直す。どのプロジェクトでも使える。
  動作確認の画像や動画を撮って自分で確認し終えたとき（タスク完了の報告の直前）に毎回使う。
  「開発履歴」「devlog」「記録して」「履歴を見たい」「履歴を開いて」でも起動。
argument-hint: "[open | list | add]"
allowed-tools: Bash, Read
---

# devlog — 動作確認の開発履歴

スクリプト：`~/.claude/skills/devlog/scripts/devlog.py`（Python 3 の標準ライブラリだけで動く）

## いつ記録するか

- 動作確認のために画像や動画を撮り、**自分で見て確認したあと**、ユーザーへ完了を報告する前に 1 回記録する
- 1 つのまとまった作業（例：aitodo の 1 タスク、または一緒に確認した数タスク）につき 1 件。途中の失敗した撮り直しは入れない
- 画像や動画を撮っていない作業（文書の修正だけなど）は記録しない

## 記録のしかた

1. 載せるファイルを選ぶ。採用した最終版だけを選ぶ：動画（15〜20 秒程度にカットしたもの）、要点がわかるスクショ、必要なら比較用の「前」の画像
2. 概要を Markdown で書く（見出し `##`、箇条書き `- `、`code`、**太字** が使える）。書く内容：
   - 何を変えたか（ユーザーの言葉でのタスク名）
   - 見て確かめたこと・テスト結果
   - 決めたこと・残っている問題
   - 長い概要は一時ファイル（スクラッチパッド）に書いて `--summary-file` で渡す
3. 実行する（プロジェクトのルートか、その中で）：

```bash
python3 ~/.claude/skills/devlog/scripts/devlog.py add \
  --title "芝生と土をそれっぽくする" \
  --summary-file /path/to/summary.md \
  --media screenshots/ground0/frame00000039.png screenshots/ground1/frame00000039.png screenshots/loop4/video.mp4 \
  --tags 見た目
```

- ファイルは `devlog/entries/<日時-タイトル>/` にコピーされるので、元の画像を後で撮り直して上書きしても履歴は変わらない
- git のブランチ・コミット・未コミットの変更があるかも自動で記録される
- 初回は `devlog/` を `.gitignore` に足す（ローカルだけに置く）。リポジトリに入れたいときは `--keep-in-git`。そのときは、スクショにトークン・個人情報・社内の画面などが写っていないか確かめてから記録する
- 報告の最後に、`devlog/index.html` の場所を 1 行で添える

## そのほかのコマンド

- `devlog.py open`：HTML を作り直してブラウザで開く（ユーザーに「履歴を見たい」と言われたとき）
- `devlog.py build`：HTML だけを作り直す（entry.json を手で直したあとなど）
- `devlog.py list`：記録の一覧

## 注意

- 動画が大きいとき（50MB を超えるなど）は、先に `ffmpeg` で短く切るか、解像度を下げてから載せる
- 記録の削除・修正は `devlog/entries/<id>/` を直接編集してから `build` する。消すときは、ユーザーに確認してから消す
