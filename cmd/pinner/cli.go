package main

import (
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"
)

// command is one subcommand. The table below is the single source of the usage text and of
// the generated documentation (docs/cli): the flags come from the same definitions that
// parse them, so the documentation cannot drift from the code.
type command struct {
	name        string
	summary     string // one line, for the usage text and the index
	description string // Markdown, for the documentation page only
	register    func(fs *flag.FlagSet)
	run         func(args []string)
	hidden      bool // not listed in the usage text and the index
}

// commands is set in init: the docs command refers to renderCLIDocs, which reads the table.
var commands []command

func init() {
	commands = []command{
		{
			name:    "collect",
			summary: "list the annotated resources and the images they use (the base of pull and trigger)",
			description: `annotation の付いたリソースと、そのリソースが使っているイメージを集めて表示する。
` + "`pull`" + ` と \` + "`trigger`" + ` は、どちらも、この収集から始まる。読み取りのみで、何も変更しない。

- 1 行が、（リソース、イメージ）。` + "`TRACKED`" + ` が ` + "`yes`" + ` のものが、` + "`pull`" + ` と ` + "`trigger`" + ` の対象。
- 対象外のイメージは、理由が出る（digest 指定、フローティングタグでない）。
- リソースの annotation に書かれたアクション（` + "`ON-MISMATCH`" + ` など）も出る。` + "`-`" + ` は未指定（既定の ` + "`none`" + `）。`,
			register: func(fs *flag.FlagSet) { registerCollect(fs) },
			run:      runCollect,
		},
		{
			name:    "pull",
			summary: "stage 1: pull into the local cache (node image stores) what changed on the remote registry",
			description: `第一段階。リモートレジストリを正にして、ローカルキャッシュ（各ノードのイメージストア）を同期する。

リモートの、フローティングタグ（既定は ` + "`latest`" + `）が指す digest と、ローカルの同じタグが指す digest を比べ、違えば、kubelet に pull させる。
pull のあとにローカルを読み直し、状態の変化（` + "`mismatch`" + ` → ` + "`match`" + `）として報告する。リソースには何もしない。

ログの行は、` + "`pull match`" + `、` + "`pull mismatch`" + ` のように、段階の接頭辞で始まる。` + "`outcome`" + ` は、pull がしたこと（` + "`pulled`" + `、` + "`would-pull`" + `、` + "`ignored`" + `、` + "`pull-failed`" + `）。`,
			register: func(fs *flag.FlagSet) { registerPull(fs) },
			run:      runPull,
		},
		{
			name:    "trigger",
			summary: "stage 2: find resources running a different image than the local cache, and run their action",
			description: `第二段階。ローカルキャッシュを正にして、実行中の Pod（Knative は Revision）の digest と比べ、違えば、リソースの annotation で指定されたアクションを依頼する。

アクションは、` + "`floatingtag-pinner/on-mismatch`" + `（任意で ` + "`on-not-pulled`" + `、` + "`on-error`" + `）で指定する。未指定は ` + "`none`" + `（何もしない）。
組み込みのアクションは ` + "`none`" + `、` + "`rollout`" + `、` + "`delete-pods`" + `。依頼したあとの結果は追わない（投げっぱなし）。

見つけたことは、アクションと関係なく、常にログに出る（` + "`trigger match`" + `、` + "`trigger mismatch`" + ` など）。`,
			register: func(fs *flag.FlagSet) { registerTrigger(fs) },
			run:      runTrigger,
		},
		{
			name:    "version",
			summary: "print which build this is (commit and Go version)",
			description: `どのビルドの ` + "`pinner`" + ` かを表示する（コミット、Go のバージョン）。不具合を報告するときや、
「修正前のバイナリを使っていないか」を確かめるときに使う。` + "`--version`" + ` でも同じ。`,
			register: func(fs *flag.FlagSet) {},
			run:      runVersion,
		},
		{
			name:     "docs",
			summary:  "write the command-line documentation (docs/cli) from the flag definitions",
			register: func(fs *flag.FlagSet) {},
			run:      runDocs,
			hidden:   true,
		},
	}
}

func usage() string {
	var b strings.Builder
	b.WriteString("usage: pinner <command> [flags]\n\ncommands:\n")
	for _, c := range commands {
		if !c.hidden {
			fmt.Fprintf(&b, "  %-8s  %s\n", c.name, c.summary)
		}
	}
	return b.String()
}

// renderCLIDocs returns the documentation pages by file name: an index, and one page per command,
// all generated from the flag definitions.
func renderCLIDocs() map[string]string {
	pages := map[string]string{}

	var idx strings.Builder
	idx.WriteString("# コマンドライン\n\n")
	idx.WriteString("このページは、コードのフラグの定義から生成している（`pinner docs`）。手で編集しない。\n\n")
	idx.WriteString("```\npinner <command> [flags]\n```\n\n| コマンド | 内容 |\n| --- | --- |\n")
	for _, c := range commands {
		if c.hidden {
			continue
		}
		fmt.Fprintf(&idx, "| [`pinner %s`](./%s.md) | %s |\n", c.name, c.name, c.summary)
	}
	pages["index.md"] = idx.String()

	for _, c := range commands {
		if c.hidden {
			continue
		}
		fs := flag.NewFlagSet("pinner "+c.name, flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		c.register(fs)

		var b strings.Builder
		fmt.Fprintf(&b, "# pinner %s\n\n%s\n\n%s\n\n", c.name, c.summary, c.description)
		fmt.Fprintf(&b, "## 使い方\n\n```\npinner %s [flags]\n```\n\n", c.name)
		b.WriteString("フラグは、環境変数でも渡せる（`PINNER_` + フラグ名の大文字、`-` は `_`）。優先順位は、コマンドライン、環境変数、既定値の順。\n\n")
		b.WriteString("## フラグ\n\n| フラグ | 環境変数 | 型 | 既定値 | 説明 |\n| --- | --- | --- | --- | --- |\n")
		var flags []*flag.Flag
		fs.VisitAll(func(f *flag.Flag) { flags = append(flags, f) })
		sort.Slice(flags, func(i, j int) bool { return flags[i].Name < flags[j].Name })
		for _, f := range flags {
			typ, usage := flag.UnquoteUsage(f)
			if typ == "" {
				typ = "bool"
			}
			def := f.DefValue
			if def == "" {
				def = "-"
			}
			fmt.Fprintf(&b, "| `%s` | `%s` | %s | `%s` | %s |\n", flagName(f.Name), envName(f.Name), typ, def, strings.ReplaceAll(usage, "|", "\\|"))
		}
		pages[c.name+".md"] = b.String()
	}
	return pages
}

func flagName(n string) string {
	if len(n) == 1 {
		return "-" + n
	}
	return "--" + n
}
