// Copyright 2015 Google Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package drive

import (
	"fmt"
	"strings"

	"github.com/odeke-em/log"
)

type attribute struct {
	minimal       bool
	mask          int
	parent        string
	diskUsageOnly bool
}

type traversalSt struct {
	file             *File
	headPath         string
	depth            int
	mask             int
	inTrash          bool
	explicitNoPrompt bool
	sorters          []string
	matchQuery       *matchQuery
}

func sorters(opts *Options) []string {
	if opts == nil || opts.Meta == nil {
		return nil
	}

	meta := *(opts.Meta)
	retr, ok := meta[SortKey]
	if !ok {
		return nil
	}

	// Keys sent in via meta need to be comma split
	// first, space trimmed then added.
	// See Issue https://github.com/odeke-em/drive/issues/714.
	var sortKeys []string
	for _, attr := range retr {
		splits := strings.Split(attr, ",")
		for _, split := range splits {
			trimmedAttr := strings.TrimSpace(split)
			sortKeys = append(sortKeys, trimmedAttr)
		}
	}

	return sortKeys
}

func (g *Commands) ListMatches() error {

	inTrash := trashed(g.opts.TypeMask)

	mq := g.createMatchQuery(false)

	mq.titleSearches = append(mq.titleSearches, fuzzyStringsValuePair{
		fuzzyLevel: Like, values: g.opts.Sources, inTrash: inTrash, joiner: Or,
	})

	pagePair := g.rem.FindMatches(mq)

	spin := g.playabler()
	spin.play()
	defer spin.stop()

	traversalCount := 0

	matches := pagePair.filesChan
	errsChan := pagePair.errsChan

	working := true
	for working {
		select {
		case err := <-errsChan:
			if err != nil {
				return err
			}
		case match, stillHasContent := <-matches:
			if !stillHasContent {
				working = false
				break
			}
			if match == nil {
				continue
			}

			travSt := traversalSt{
				depth:    g.opts.Depth,
				file:     match,
				headPath: g.opts.Path,
				inTrash:  g.opts.InTrash,
				mask:     g.opts.TypeMask,
				sorters:  sorters(g.opts),
			}

			traversalCount += 1

			if !g.breadthFirst(travSt, spin) {
				break
			}
		}
	}

	if traversalCount < 1 {
		g.log.LogErrln("no matches found!")
	}

	return nil
}

func (g *Commands) createMatchQuery(exactMatch bool) *matchQuery {

	mimeQuerySearches := []fuzzyStringsValuePair{}
	titleSearches := []fuzzyStringsValuePair{}
	ownerSearches := []fuzzyStringsValuePair{}

	if g.opts.Meta != nil {
		meta := *(g.opts.Meta)
		skipMimes, sOk := meta[SkipMimeKeyKey]
		if sOk {
			mimeQuerySearches = append(mimeQuerySearches, fuzzyStringsValuePair{
				fuzzyLevel: Not, values: skipMimes, inTrash: g.opts.InTrash, joiner: And,
			})
		}

		matchMimes, mOk := meta[MatchMimeKeyKey]
		if mOk {
			mimeQuerySearches = append(mimeQuerySearches, fuzzyStringsValuePair{
				fuzzyLevel: Is, values: matchMimes, inTrash: g.opts.InTrash, joiner: Or,
			})
		}

		exactTitles, etOk := meta[ExactTitleKey]
		if etOk {
			titleSearches = append(titleSearches, fuzzyStringsValuePair{
				fuzzyLevel: Is, values: exactTitles, inTrash: g.opts.InTrash, joiner: Or,
			})
		}

		exactOwners, eoOk := meta[ExactOwnerKey]
		if eoOk {
			ownerSearches = append(ownerSearches, fuzzyStringsValuePair{
				fuzzyLevel: Is, values: exactOwners, joiner: Or,
			})
		}

		matchOwners, moOk := meta[MatchOwnerKey]
		if moOk {
			ownerSearches = append(ownerSearches, fuzzyStringsValuePair{
				fuzzyLevel: Like, values: matchOwners, joiner: Or,
			})
		}

		notOwner, soOk := meta[NotOwnerKey]
		if soOk {
			ownerSearches = append(ownerSearches, fuzzyStringsValuePair{
				fuzzyLevel: NotIn, values: notOwner, joiner: And,
			})
		}
	}

	mq := matchQuery{
		dirPath:           g.opts.Path,
		inTrash:           g.opts.InTrash,
		mimeQuerySearches: mimeQuerySearches,
		titleSearches:     titleSearches,
		ownerSearches:     ownerSearches,
	}

	return &mq
}

func (g *Commands) List(byId bool) error {
	var kvList []*keyValue

	resolver := g.rem.FindByPath
	if byId {
		resolver = g.rem.FindById
	}

	mq := g.createMatchQuery(true)

	for i, relPath := range g.opts.Sources {
		r, rErr := resolver(relPath)
		g.DebugPrintf("[Commands.List] #%d %q\n", i, relPath)
		if rErr != nil && rErr != ErrPathNotExists {
			return illogicalStateErr(fmt.Errorf("%v: '%s'", rErr, relPath))
		}

		if r == nil {
			g.log.LogErrf("%s cannot be found remotely\n", customQuote(relPath))
			continue
		}

		parentPath := ""
		if !byId {
			parentPath = g.parentPather(relPath)
		} else {
			parentPath = r.Id
		}

		if remoteRootLike(parentPath) {
			parentPath = ""
		}
		if remoteRootLike(r.Name) {
			r.Name = ""
		}
		if rootLike(parentPath) {
			parentPath = ""
		}

		kvList = append(kvList, &keyValue{key: parentPath, value: r})
	}

	spin := g.playabler()
	spin.play()
	for _, kv := range kvList {
		if kv == nil || kv.value == nil {
			continue
		}

		travSt := traversalSt{
			depth:      g.opts.Depth,
			file:       kv.value.(*File),
			headPath:   kv.key,
			inTrash:    g.opts.InTrash,
			mask:       g.opts.TypeMask,
			sorters:    sorters(g.opts),
			matchQuery: mq,
		}

		if !g.breadthFirst(travSt, spin) {
			break
		}
	}
	spin.stop()

	return nil
}

func (g *Commands) listSharedPerPath(relToRootPath string) ([]*keyValue, error) {
	pagePair := g.rem.FindByPathShared(relToRootPath)
	errsChan := pagePair.errsChan
	sharedRemotes := pagePair.filesChan

	var kvList []*keyValue

	working := true
	for working {
		select {
		case err := <-errsChan:
			if err != nil {
				g.log.LogErrf("%v: '%s'\n", err, relToRootPath)
				return kvList, err
			}
		case s, stillHasContent := <-sharedRemotes:
			if !stillHasContent {
				working = false
				break
			}

			parentPath := g.parentPather(relToRootPath)

			if remoteRootLike(parentPath) {
				parentPath = ""
			}

			if rootLike(parentPath) {
				parentPath = ""
			}

			if s == nil {
				continue
			}

			if remoteRootLike(s.Name) {
				s.Name = ""
			}

			kvList = append(kvList, &keyValue{key: parentPath, value: s})
		}
	}

	return kvList, nil
}

func (g *Commands) ListShared() (err error) {
	spin := g.playabler()
	spin.play()
	defer spin.stop()

	var kvList []*keyValue

	for _, relPath := range g.opts.Sources {
		childKvList, err := g.listSharedPerPath(relPath)
		if err != nil {
			return err
		}
		kvList = append(kvList, childKvList...)
	}

	for _, kv := range kvList {
		if kv == nil || kv.value == nil {
			continue
		}

		travSt := traversalSt{
			depth:    g.opts.Depth,
			file:     kv.value.(*File),
			headPath: kv.key,
			inTrash:  g.opts.InTrash,
			mask:     g.opts.TypeMask,
		}

		if !g.breadthFirst(travSt, spin) {
			break
		}
	}
	spin.stop()
	return
}

func (f *File) pretty(logy *log.Logger, opt attribute) {
	fmtdPath := sepJoin("/", opt.parent, f.Name)

	if opt.diskUsageOnly {
		logy.Logf("%-12v %s\n", f.Size, fmtdPath)
		return
	}

	if opt.minimal {
		logy.Logf("%s", fmtdPath)
	} else {
		if f.IsDir {
			logy.Logf("d")
		} else {
			logy.Logf("-")
		}
		if f.Shared {
			logy.Logf("s")
		} else {
			logy.Logf("-")
		}

		if f.UserPermission != nil {
			logy.Logf(" %-10s ", f.UserPermission.Role)
		}
	}

	if owners(opt.mask) && len(f.OwnerNames) >= 1 {
		logy.Logf(" %s ", strings.Join(f.OwnerNames, " & "))
	}

	if version(opt.mask) {
		logy.Logf(" v%d", f.Version)
	}

	if !opt.minimal {
		logy.Logf(" %-10s\t%-10s\t\t%-20s\t%-s\n", prettyBytes(f.Size), f.Id, f.ModTime, fmtdPath)
	} else {
		logy.Logln()
	}
}

func (g *Commands) paginator(f *File, travSt traversalSt) func() *paginationPair {
	expr := buildExpression(f.Id, travSt.mask, travSt.inTrash)

	if mq := travSt.matchQuery; mq != nil {
		exprExtra := mq.Stringer()
		expr = sepJoinNonEmpty(" and ", fmt.Sprintf("(%s)", expr), exprExtra)
	}

	var paginator func() *paginationPair
	if teamDrives(g.opts.TypeMask) {
		req := g.rem.service.Teamdrives.List()
		req.Q(expr)
		req.MaxResults(g.opts.PageSize)
		paginator = func() *paginationPair {
			return reqPageTeamDrives(req, g.opts.Hidden, false)
		}
	} else {
		req := g.rem.service.Files.List()
		req.Q(expr)
		req.MaxResults(g.opts.PageSize)
		paginator = func() *paginationPair {
			return reqDoPage(req, g.opts.Hidden, false)
		}
	}

	return paginator
}

func (g *Commands) breadthFirst(travSt traversalSt, spin *playable) bool {
	opt := attribute{
		minimal:       isMinimal(g.opts.TypeMask),
		diskUsageOnly: diskUsageOnly(g.opts.TypeMask),
		mask:          travSt.mask,
	}

	opt.parent = ""
	if travSt.headPath != "/" {
		opt.parent = travSt.headPath
	}

	f := travSt.file
	if !f.IsDir {
		f.pretty(g.log, opt)
		return true
	}

	// New head path
	if !(rootLike(opt.parent) && rootLike(f.Name)) {
		opt.parent = sepJoin("/", opt.parent, f.Name)
	}

	// A depth of < 0 means traverse as deep as you can
	if travSt.depth == 0 {
		// At the end of the line, this was successful.
		return true
	} else if travSt.depth > 0 {
		travSt.depth -= 1
	}

	spin.pause()

	canPrompt := !travSt.explicitNoPrompt
	if canPrompt {
		canPrompt = g.opts.canPrompt()
	}

	spin.play()

	onlyFiles := nonFolderExplicitly(g.opts.TypeMask)

	iterCount := uint64(0)

	var collector []*File

	// We shouldn't prompt in between the same page otherwise we get
	// spurious prompts. See Issue https://github.com/odeke-em/drive/issues/724.
	// We'll only make the prompts in between children.
	pagePair := g.paginator(f, travSt)
	errsChan := pagePair.errsChan
	filesChan := pagePair.filesChan

	working := true
	for working {
		select {
		case err := <-errsChan:
			if err != nil {
				g.log.LogErrf("%v", err)
				return false
			}
		case file, stillHasContent := <-filesChan:
			if !stillHasContent {
				working = false
				break
			}
			if file == nil {
				return false
			}

			if !isHidden(file.Name, g.opts.Hidden) {
				collector = append(collector, file)
			}

		}
	}

	if len(travSt.sorters) >= 1 {
		collector = g.sort(collector, travSt.sorters...)
	}

	var children []*File
	for _, file := range collector {
		if file.IsDir {
			children = append(children, file)
		}

		// The case in which only directories wanted is covered by the buildExpression clause
		// reason being that only folder are allowed to be roots, including the only files clause
		// would result in incorrect traversal since non-folders don't have children.
		// Just don't print it, however, the folder will still be explored.
		if onlyFiles && file.IsDir {
			continue
		}
		file.pretty(g.log, opt)
		iterCount += 1
	}

	if !travSt.inTrash && !g.opts.InTrash {
		// We'll only prompt when traversing children to avoid
		// spurious prompts that result from asynchronous paging
		// before children have been retrieved, sorted and printed.
		// See Issue https://github.com/odeke-em/drive/issues/724.
		canPage := travSt.depth != 0 && len(children) > 0
		if canPage && canPrompt && !nextPage() {
			return false
		}

		for _, file := range children {
			childSt := traversalSt{
				depth:            travSt.depth,
				file:             file,
				headPath:         opt.parent,
				inTrash:          travSt.inTrash,
				mask:             g.opts.TypeMask,
				explicitNoPrompt: travSt.explicitNoPrompt,
				sorters:          travSt.sorters,
				matchQuery:       travSt.matchQuery,
			}

			if !g.breadthFirst(childSt, spin) {
				return false
			}
		}
		return true
	}

	return iterCount >= 1
}

func diskUsageOnly(mask int) bool {
	return (mask & DiskUsageOnly) != 0
}

func isMinimal(mask int) bool {
	return (mask & Minimal) != 0
}

func owners(mask int) bool {
	return (mask & Owners) != 0
}

func version(mask int) bool {
	return (mask & CurrentVersion) != 0
}

func shared(mask int) bool {
	return (mask & Shared) != 0
}

func trashed(mask int) bool {
	return (mask & InTrash) != 0
}

func starred(mask int) bool {
	return (mask & Starred) != 0
}

func teamDrives(mask int) bool {
	return (mask & TeamDrives) != 0
}
