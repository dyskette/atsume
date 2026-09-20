package scraper

import lua "github.com/yuin/gopher-lua"

// MangaInfo is the MANGAINFO object: the result of a module's GetInfo handler.
type MangaInfo struct {
	URL          string
	Title        string
	AltTitles    string
	CoverLink    string
	Authors      string
	Artists      string
	Genres       string
	Status       string
	Summary      string
	ChapterLinks *Strings
	ChapterNames *Strings
}

// NewMangaInfo returns an empty MANGAINFO with its lists allocated.
func NewMangaInfo() *MangaInfo {
	return &MangaInfo{ChapterLinks: NewStrings(), ChapterNames: NewStrings()}
}

func (m *MangaInfo) bind(L *lua.LState) lua.LValue {
	f := newFields("atsume.MangaInfo")
	f.str["URL"] = &m.URL
	f.str["Title"] = &m.Title
	f.str["AltTitles"] = &m.AltTitles
	f.str["CoverLink"] = &m.CoverLink
	f.str["Authors"] = &m.Authors
	f.str["Artists"] = &m.Artists
	f.str["Genres"] = &m.Genres
	f.str["Status"] = &m.Status
	f.str["Summary"] = &m.Summary
	f.list["ChapterLinks"] = m.ChapterLinks
	f.list["ChapterNames"] = m.ChapterNames
	return f.push(L)
}

// Task is the TASK object: the page list a module builds for one chapter.
type Task struct {
	Link                      string
	PageNumber                int
	CurrentDownloadChapterPtr int
	PageLinks                 *Strings
	PageContainerLinks        *Strings
	FileNames                 *Strings
	ChapterLinks              *Strings
	ChapterNames              *Strings
}

// NewTask returns an empty TASK with its lists allocated.
func NewTask() *Task {
	return &Task{
		PageLinks:          NewStrings(),
		PageContainerLinks: NewStrings(),
		FileNames:          NewStrings(),
		ChapterLinks:       NewStrings(),
		ChapterNames:       NewStrings(),
	}
}

func (t *Task) bind(L *lua.LState) lua.LValue {
	f := newFields("atsume.Task")
	f.str["Link"] = &t.Link
	f.num["PageNumber"] = &t.PageNumber
	f.num["CurrentDownloadChapterPtr"] = &t.CurrentDownloadChapterPtr
	f.list["PageLinks"] = t.PageLinks
	f.list["PageContainerLinks"] = t.PageContainerLinks
	f.list["FileNames"] = t.FileNames
	f.list["ChapterLinks"] = t.ChapterLinks
	f.list["ChapterNames"] = t.ChapterNames
	return f.push(L)
}

// updateList is the UPDATELIST object. Only the directory page counter and the
// status callback are used by modules.
type updateList struct {
	CurrentDirectoryPageNumber int
	onStatus                   func(string)
}

func (u *updateList) bind(L *lua.LState) lua.LValue {
	f := newFields("atsume.UpdateList")
	f.num["CurrentDirectoryPageNumber"] = &u.CurrentDirectoryPageNumber
	f.methods["UpdateStatusText"] = func(L *lua.LState) int {
		if u.onStatus != nil {
			u.onStatus(L.CheckString(1))
		}
		return 0
	}
	return f.push(L)
}
