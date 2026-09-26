package scraper

import (
	rt "github.com/arnodel/golua/runtime"
)

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

// updateList is the UPDATELIST object. Only the directory page counter and the
// status callback are used by modules.
type updateList struct {
	CurrentDirectoryPageNumber int
	onStatus                   func(string)
}

func (m *MangaInfo) bind(r *rt.Runtime) rt.Value {
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
	return f.push(r)
}

func (t *Task) bind(r *rt.Runtime) rt.Value {
	f := newFields("atsume.Task")
	f.str["Link"] = &t.Link
	f.num["PageNumber"] = &t.PageNumber
	f.num["CurrentDownloadChapterPtr"] = &t.CurrentDownloadChapterPtr
	f.list["PageLinks"] = t.PageLinks
	f.list["PageContainerLinks"] = t.PageContainerLinks
	f.list["FileNames"] = t.FileNames
	f.list["ChapterLinks"] = t.ChapterLinks
	f.list["ChapterNames"] = t.ChapterNames
	return f.push(r)
}

func (u *updateList) bind(r *rt.Runtime) rt.Value {
	f := newFields("atsume.UpdateList")
	f.num["CurrentDirectoryPageNumber"] = &u.CurrentDirectoryPageNumber
	f.methods["UpdateStatusText"] = goFn{1, func(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
		s, err := checkString(c, 0)
		if err != nil {
			return nil, err
		}
		if u.onStatus != nil {
			u.onStatus(s)
		}
		return c.Next(), nil
	}}
	return f.push(r)
}

// DirectoryPageNumber is how many pages the module said the current directory
// has, through UPDATELIST.CurrentDirectoryPageNumber; 0 when it said nothing.
func (r *Runner) DirectoryPageNumber() int { return r.update.CurrentDirectoryPageNumber }
