package ui

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/paginator"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/log"
	"github.com/muesli/reflow/ansi"
	"github.com/muesli/reflow/truncate"
	"github.com/sahilm/fuzzy"
)

const (
	stashIndent                = 1
	stashViewItemHeight        = 3 // height of stash entry, including gap
	stashViewTopPadding        = 5 // logo, status bar, gaps
	stashViewBottomPadding     = 3 // pagination and gaps, but not help
	stashViewHorizontalPadding = 6
)

var stashingStatusMessage = statusMessage{normalStatusMessage, "Stashing..."}

var (
	dividerDot = darkGrayFg.SetString(" • ")
	dividerBar = darkGrayFg.SetString(" │ ")

	logoStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#ECFD65")).
			Background(fuchsia).
			Bold(true)

	stashSpinnerStyle = lipgloss.NewStyle().
				Foreground(gray)
	stashInputPromptStyle = lipgloss.NewStyle().
				Foreground(yellowGreen).
				MarginRight(1)
	stashInputCursorStyle = lipgloss.NewStyle().
				Foreground(fuchsia).
				MarginRight(1)
)

// MSG

type (
	filteredMarkdownMsg []*markdown
	fetchedMarkdownMsg  *markdown
)

// MODEL

// stashViewState is the high-level state of the file listing.
type stashViewState int

const (
	stashStateReady stashViewState = iota
	stashStateLoadingDocument
	stashStateShowingError
)

// The types of documents we are currently showing to the user.
type sectionKey int

const (
	documentsSection = iota
	filterSection
)

// section contains definitions and state information for displaying a tab and
// its contents in the file listing view.
type section struct {
	key       sectionKey
	paginator paginator.Model
	cursor    int
}

// map sections to their associated types.
var sections = map[sectionKey]section{}

// filterState is the current filtering state in the file listing.
type filterState int

const (
	unfiltered    filterState = iota // no filter set
	filtering                        // user is actively setting a filter
	filterApplied                    // a filter is applied and user is not editing filter
)

// statusMessageType adds some context to the status message being sent.
type statusMessageType int

// Types of status messages.
const (
	normalStatusMessage statusMessageType = iota
	subtleStatusMessage
	errorStatusMessage
)

// statusMessage is an ephemeral note displayed in the UI.
type statusMessage struct {
	status  statusMessageType
	message string
}

func initSections() {
	sections = map[sectionKey]section{
		documentsSection: {
			key:       documentsSection,
			paginator: newStashPaginator(),
		},
		filterSection: {
			key:       filterSection,
			paginator: newStashPaginator(),
		},
	}
}

// String returns a styled version of the status message appropriate for the
// given context.
func (s statusMessage) String() string {
	switch s.status { //nolint:exhaustive
	case subtleStatusMessage:
		return dimGreenFg(s.message)
	case errorStatusMessage:
		return redFg(s.message)
	default:
		return greenFg(s.message)
	}
}

type stashModel struct {
	common             *commonModel
	err                error
	spinner            spinner.Model
	filterInput        textinput.Model
	viewState          stashViewState
	filterState        filterState
	showFullHelp       bool
	showStatusMessage  bool
	statusMessage      statusMessage
	statusMessageTimer *time.Timer

	// Available document sections we can cycle through. We use a slice, rather
	// than a map, because order is important.
	sections []section

	// Index of the section we're currently looking at
	sectionIndex int

	// Tracks if docs were loaded
	loaded bool

	// Tree browser state — built once loading completes.
	dirIndex   map[string][]treeEntry
	currentDir string

	// The master set of markdown documents we're working with.
	markdowns []*markdown

	// Markdown documents we're currently displaying. Filtering, toggles and so
	// on will alter this slice so we can show what is relevant. For that
	// reason, this field should be considered ephemeral.
	filteredMarkdowns []*markdown

	// Page we're fetching stash items from on the server, which is different
	// from the local pagination. Generally, the server will return more items
	// than we can display at a time so we can paginate locally without having
	// to fetch every time.
	serverPage int64
}

func (m stashModel) loadingDone() bool {
	return m.loaded
}

// currentEntries returns the entries for the directory currently being browsed,
// filtered by the active search if one is set.
func (m stashModel) currentEntries() []treeEntry {
	if m.dirIndex == nil {
		return nil
	}
	entries := m.dirIndex[m.currentDir]
	if m.filterState == unfiltered || m.filterInput.Value() == "" {
		return entries
	}
	val := strings.ToLower(m.filterInput.Value())
	var filtered []treeEntry
	for _, e := range entries {
		if strings.Contains(strings.ToLower(e.name), val) {
			filtered = append(filtered, e)
		}
	}
	return filtered
}

// selectedEntry returns the currently highlighted tree entry.
func (m stashModel) selectedEntry() *treeEntry {
	entries := m.currentEntries()
	i := m.paginator().Page*m.paginator().PerPage + m.cursor()
	if i < 0 || len(entries) == 0 || i >= len(entries) {
		return nil
	}
	return &entries[i]
}

func (m stashModel) currentSection() *section {
	return &m.sections[m.sectionIndex]
}

func (m stashModel) paginator() *paginator.Model {
	return &m.currentSection().paginator
}

func (m *stashModel) setPaginator(p paginator.Model) {
	m.currentSection().paginator = p
}

func (m stashModel) cursor() int {
	return m.currentSection().cursor
}

func (m *stashModel) setCursor(i int) {
	m.currentSection().cursor = i
}

// Whether or not the spinner should be spinning.
func (m stashModel) shouldSpin() bool {
	loading := !m.loadingDone()
	openingDocument := m.viewState == stashStateLoadingDocument
	return loading || openingDocument
}

func (m *stashModel) setSize(width, height int) {
	m.common.width = width
	m.common.height = height

	m.filterInput.Width = width - stashViewHorizontalPadding*2 - ansi.PrintableRuneWidth(
		m.filterInput.Prompt,
	)

	m.updatePagination()
}

func (m *stashModel) resetFiltering() {
	m.filterState = unfiltered
	m.filterInput.Reset()
	m.filteredMarkdowns = nil

	sortMarkdowns(m.markdowns)

	// If the filtered section is present (it's always at the end) slice it out
	// of the sections slice to remove it from the UI.
	if m.sections[len(m.sections)-1].key == filterSection {
		m.sections = m.sections[:len(m.sections)-1]
	}

	// If the current section is out of bounds (it would be if we cut down the
	// slice above) then return to the first section.
	if m.sectionIndex > len(m.sections)-1 {
		m.sectionIndex = 0
	}

	// Update pagination after we've switched sections.
	m.updatePagination()
}

// Is a filter currently being applied?
func (m stashModel) filterApplied() bool {
	return m.filterState != unfiltered
}

// Should we be updating the filter?
func (m stashModel) shouldUpdateFilter() bool {
	// If we're in the middle of setting a note don't update the filter so that
	// the focus won't jump around.
	return m.filterApplied()
}

// Update pagination according to the amount of markdowns for the current
// state.
func (m *stashModel) updatePagination() {
	_, helpHeight := m.helpView()

	availableHeight := m.common.height -
		stashViewTopPadding -
		helpHeight -
		stashViewBottomPadding

	m.paginator().PerPage = max(1, availableHeight/stashViewItemHeight)

	if pages := len(m.currentEntries()); pages < 1 {
		m.paginator().SetTotalPages(1)
	} else {
		m.paginator().SetTotalPages(pages)
	}

	// Make sure the page stays in bounds
	if m.paginator().Page >= m.paginator().TotalPages-1 {
		m.paginator().Page = max(0, m.paginator().TotalPages-1)
	}
}

// MarkdownIndex returns the index of the currently selected markdown item.
func (m stashModel) markdownIndex() int {
	return m.paginator().Page*m.paginator().PerPage + m.cursor()
}

// Return the current selected markdown in the stash.
func (m stashModel) selectedMarkdown() *markdown {
	i := m.markdownIndex()

	mds := m.getVisibleMarkdowns()
	if i < 0 || len(mds) == 0 || len(mds) <= i {
		return nil
	}

	return mds[i]
}

// Adds markdown documents to the model.
func (m *stashModel) addMarkdowns(mds ...*markdown) {
	if len(mds) == 0 {
		return
	}

	m.markdowns = append(m.markdowns, mds...)
	if !m.filterApplied() {
		sortMarkdowns(m.markdowns)
	}

	m.updatePagination()
}

// Returns the markdowns that should be currently shown.
func (m stashModel) getVisibleMarkdowns() []*markdown {
	if m.filterState == filtering || m.currentSection().key == filterSection {
		return m.filteredMarkdowns
	}

	return m.markdowns
}

// Command for opening a markdown document in the pager. Note that this also
// alters the model.
func (m *stashModel) openMarkdown(md *markdown) tea.Cmd {
	m.viewState = stashStateLoadingDocument
	cmd := loadLocalMarkdown(md)
	return tea.Batch(cmd, m.spinner.Tick)
}

func (m *stashModel) hideStatusMessage() {
	m.showStatusMessage = false
	m.statusMessage = statusMessage{}
	if m.statusMessageTimer != nil {
		m.statusMessageTimer.Stop()
	}
}

func (m *stashModel) moveCursorUp() {
	m.setCursor(m.cursor() - 1)
	if m.cursor() < 0 && m.paginator().Page == 0 {
		// Stop
		m.setCursor(0)
		return
	}

	if m.cursor() >= 0 {
		return
	}
	// Go to previous page
	m.paginator().PrevPage()

	m.setCursor(m.paginator().ItemsOnPage(len(m.currentEntries())) - 1)
}

func (m *stashModel) moveCursorDown() {
	itemsOnPage := m.paginator().ItemsOnPage(len(m.currentEntries()))

	m.setCursor(m.cursor() + 1)
	if m.cursor() < itemsOnPage {
		return
	}

	if !m.paginator().OnLastPage() {
		m.paginator().NextPage()
		m.setCursor(0)
		return
	}

	if m.cursor() > itemsOnPage {
		m.setCursor(0)
		return
	}
	m.setCursor(itemsOnPage - 1)
}

// INIT

func newStashModel(common *commonModel) stashModel {
	sp := spinner.New()
	sp.Spinner = spinner.Line
	sp.Style = stashSpinnerStyle

	si := textinput.New()
	si.Prompt = "Find:"
	si.PromptStyle = stashInputPromptStyle
	si.Cursor.Style = stashInputCursorStyle
	si.Focus()

	s := []section{
		sections[documentsSection],
	}

	m := stashModel{
		common:      common,
		spinner:     sp,
		filterInput: si,
		serverPage:  1,
		sections:    s,
	}

	return m
}

func newStashPaginator() paginator.Model {
	p := paginator.New()
	p.Type = paginator.Dots
	p.ActiveDot = brightGrayFg("•")
	p.InactiveDot = darkGrayFg.Render("•")
	return p
}

// UPDATE

func (m stashModel) update(msg tea.Msg) (stashModel, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case errMsg:
		m.err = msg

	case localFileSearchFinished:
		// We're finished searching for local files
		m.loaded = true
		m.dirIndex = buildDirIndex(m.markdowns)
		m.setCursor(0)
		m.paginator().Page = 0
		m.updatePagination()

	case filteredMarkdownMsg:
		m.filteredMarkdowns = msg
		m.setCursor(0)
		return m, nil

	case spinner.TickMsg:
		if m.shouldSpin() {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			cmds = append(cmds, cmd)
		}

	case statusMessageTimeoutMsg:
		if applicationContext(msg) == stashContext {
			m.hideStatusMessage()
		}
	}

	if m.filterState == filtering {
		cmds = append(cmds, m.handleFiltering(msg))
		return m, tea.Batch(cmds...)
	}

	// Updates per the current state
	switch m.viewState { //nolint:exhaustive
	case stashStateReady:
		cmds = append(cmds, m.handleDocumentBrowsing(msg))
	case stashStateShowingError:
		// Any key exists the error view
		if _, ok := msg.(tea.KeyMsg); ok {
			m.viewState = stashStateReady
		}
	}

	return m, tea.Batch(cmds...)
}

// Updates for when a user is browsing the markdown listing.
func (m *stashModel) handleDocumentBrowsing(msg tea.Msg) tea.Cmd {
	var cmds []tea.Cmd

	numDocs := len(m.currentEntries())

	switch msg := msg.(type) {
	// Handle keys
	case tea.KeyMsg:
		switch msg.String() {
		case "k", "ctrl+k", "up":
			m.moveCursorUp()

		case "j", "ctrl+j", "down":
			m.moveCursorDown()

		// Go to the very start
		case "home", "g":
			m.paginator().Page = 0
			m.setCursor(0)

		// Go to the very end
		case "end", "G":
			m.paginator().Page = m.paginator().TotalPages - 1
			m.setCursor(m.paginator().ItemsOnPage(numDocs) - 1)

		// Clear filter (if applicable)
		case keyEsc:
			if m.filterApplied() {
				m.resetFiltering()
			}

		// Next section
		case "tab", "L":
			if len(m.sections) == 0 || m.filterState == filtering {
				break
			}
			m.sectionIndex++
			if m.sectionIndex >= len(m.sections) {
				m.sectionIndex = 0
			}
			m.updatePagination()

		// Previous section
		case "shift+tab", "H":
			if len(m.sections) == 0 || m.filterState == filtering {
				break
			}
			m.sectionIndex--
			if m.sectionIndex < 0 {
				m.sectionIndex = len(m.sections) - 1
			}
			m.updatePagination()

		case "F":
			m.loaded = false
			return findLocalFiles(*m.common)

		// Edit document in EDITOR
		case "e":
			entry := m.selectedEntry()
			if entry == nil || entry.kind == entryDir {
				return nil
			}
			return openEditor(entry.md.localPath, 0)

		// Navigate into directory or open document
		case keyEnter, "l", "right":
			m.hideStatusMessage()

			if numDocs == 0 {
				break
			}

			entry := m.selectedEntry()
			if entry == nil {
				break
			}
			if entry.kind == entryDir {
				if m.currentDir == "" {
					m.currentDir = entry.name
				} else {
					m.currentDir = filepath.Join(m.currentDir, entry.name)
				}
				m.setCursor(0)
				m.paginator().Page = 0
				m.updatePagination()
				if m.filterApplied() {
					m.resetFiltering()
				}
			} else {
				cmds = append(cmds, m.openMarkdown(entry.md))
			}

		// Go up one directory level
		case "h", "left":
			if m.currentDir != "" {
				m.currentDir = filepath.Dir(m.currentDir)
				if m.currentDir == "." {
					m.currentDir = ""
				}
				m.setCursor(0)
				m.paginator().Page = 0
				m.updatePagination()
				if m.filterApplied() {
					m.resetFiltering()
				}
			}

		// Filter your notes
		case "/":
			m.hideStatusMessage()

			// Build values we'll filter against
			for _, md := range m.markdowns {
				md.buildFilterValue()
			}

			m.filteredMarkdowns = m.markdowns

			m.paginator().Page = 0
			m.setCursor(0)
			m.filterState = filtering
			m.filterInput.CursorEnd()
			m.filterInput.Focus()
			return textinput.Blink

		// Toggle full help
		case "?":
			m.showFullHelp = !m.showFullHelp
			m.updatePagination()

		// Show errors
		case "!":
			if m.err != nil && m.viewState == stashStateReady {
				m.viewState = stashStateShowingError
				return nil
			}
		}
	}

	// Update paginator. Pagination key handling is done here, but it could
	// also be moved up to this level, in which case we'd use model methods
	// like model.PageUp().
	newPaginatorModel, cmd := m.paginator().Update(msg)
	m.setPaginator(newPaginatorModel)
	cmds = append(cmds, cmd)

	// Extra paginator keystrokes
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "b", "u":
			m.paginator().PrevPage()
		case "f", "d":
			m.paginator().NextPage()
		}
	}

	// Keep the index in bounds when paginating
	itemsOnPage := m.paginator().ItemsOnPage(len(m.currentEntries()))
	if m.cursor() > itemsOnPage-1 {
		m.setCursor(max(0, itemsOnPage-1))
	}

	return tea.Batch(cmds...)
}

// Updates for when a user is in the filter editing interface.
func (m *stashModel) handleFiltering(msg tea.Msg) tea.Cmd {
	var cmds []tea.Cmd

	// Handle keys
	if msg, ok := msg.(tea.KeyMsg); ok { //nolint:nestif
		switch msg.String() {
		case keyEsc:
			m.resetFiltering()
		case keyEnter, "tab", "shift+tab", "ctrl+k", "up", "ctrl+j", "down":
			m.hideStatusMessage()

			entries := m.currentEntries()

			if len(entries) == 0 {
				m.viewState = stashStateReady
				m.resetFiltering()
				break
			}

			// When there's exactly one file result, open it directly.
			if len(entries) == 1 && entries[0].kind == entryFile {
				m.viewState = stashStateReady
				m.resetFiltering()
				cmds = append(cmds, m.openMarkdown(entries[0].md))
				break
			}

			m.filterInput.Blur()
			m.filterState = filterApplied
			if m.filterInput.Value() == "" {
				m.resetFiltering()
			}
		}
	}

	// Update the filter text input component
	newFilterInputModel, inputCmd := m.filterInput.Update(msg)
	currentFilterVal := m.filterInput.Value()
	newFilterVal := newFilterInputModel.Value()
	m.filterInput = newFilterInputModel
	cmds = append(cmds, inputCmd)

	if newFilterVal != currentFilterVal {
		m.setCursor(0)
		m.paginator().Page = 0
		m.updatePagination()
	}

	m.updatePagination()
	return tea.Batch(cmds...)
}

// VIEW

func (m stashModel) view() string {
	var s string
	switch m.viewState {
	case stashStateShowingError:
		return errorView(m.err, false)
	case stashStateLoadingDocument:
		s += " " + m.spinner.View() + " Loading document..."
	case stashStateReady:
		loadingIndicator := " "
		if m.shouldSpin() {
			loadingIndicator = m.spinner.View()
		}

		// Only draw the normal header if we're not using the header area for
		// something else (like a note or delete prompt).
		header := m.headerView()

		// Rules for the logo, filter and status message.
		logoOrFilter := " "
		if m.showStatusMessage && m.filterState == filtering {
			logoOrFilter += m.statusMessage.String()
		} else if m.filterState == filtering {
			logoOrFilter += m.filterInput.View()
		} else {
			logoOrFilter += glowLogoView()
			if m.showStatusMessage {
				logoOrFilter += "  " + m.statusMessage.String()
			}
		}
		logoOrFilter = truncate.StringWithTail(logoOrFilter, uint(m.common.width-1), ellipsis) //nolint:gosec

		help, helpHeight := m.helpView()

		populatedView := m.populatedView()
		populatedViewHeight := strings.Count(populatedView, "\n") + 2

		// We need to fill any empty height with newlines so the footer reaches
		// the bottom.
		availHeight := m.common.height -
			stashViewTopPadding -
			populatedViewHeight -
			helpHeight -
			stashViewBottomPadding
		blankLines := strings.Repeat("\n", max(0, availHeight))

		var pagination string
		if m.paginator().TotalPages > 1 {
			pagination = m.paginator().View()

			// If the dot pagination is wider than the width of the window
			// use the arabic paginator.
			if ansi.PrintableRuneWidth(pagination) > m.common.width-stashViewHorizontalPadding {
				// Copy the paginator since m.paginator() returns a pointer to
				// the active paginator and we don't want to mutate it. In
				// normal cases, where the paginator is not a pointer, we could
				// safely change the model parameters for rendering here as the
				// current model is discarded after reuturning from a View().
				// One could argue, in fact, that using pointers in
				// a functional framework is an antipattern and our use of
				// pointers in our model should be refactored away.
				p := *(m.paginator())
				p.Type = paginator.Arabic
				pagination = paginationStyle.Render(p.View())
			}
		}

		s += fmt.Sprintf(
			"%s%s\n\n  %s\n\n%s\n\n%s  %s\n\n%s",
			loadingIndicator,
			logoOrFilter,
			header,
			populatedView,
			blankLines,
			pagination,
			help,
		)
	}
	return "\n" + indent(s, stashIndent)
}

func glowLogoView() string {
	return logoStyle.Render(" Glow ")
}

func (m stashModel) headerView() string {
	localCount := len(m.markdowns)

	var sections []string //nolint:prealloc

	// Filter results
	if m.filterState == filtering {
		if localCount == 0 {
			return grayFg("Nothing found.")
		}
		if localCount > 0 {
			sections = append(sections, fmt.Sprintf("%d local", localCount))
		}

		for i := range sections {
			sections[i] = grayFg(sections[i])
		}

		return strings.Join(sections, dividerDot.String())
	}

	// Show path breadcrumb or total document count at root.
	var label string
	if m.currentDir == "" {
		label = fmt.Sprintf("%d documents", localCount)
	} else {
		label = "/" + m.currentDir
	}
	sections = append(sections, tabStyle.Render(label))

	return strings.Join(sections, dividerBar.String())
}

func (m stashModel) populatedView() string {
	entries := m.currentEntries()

	var b strings.Builder

	if len(entries) == 0 {
		if m.loadingDone() {
			b.WriteString("  " + grayFg("No files found."))
		} else {
			b.WriteString("  " + grayFg("Looking for local files..."))
		}
		// pad remaining space
		n := m.paginator().PerPage*stashViewItemHeight - (stashViewItemHeight - 1)
		for i := 0; i < n; i++ {
			fmt.Fprint(&b, "\n")
		}
		return b.String()
	}

	start, end := m.paginator().GetSliceBounds(len(entries))
	page := entries[start:end]

	for i, entry := range page {
		treeEntryView(&b, m, i, entry)
		if i != len(page)-1 {
			fmt.Fprintf(&b, "\n\n")
		}
	}

	itemsOnPage := m.paginator().ItemsOnPage(len(entries))
	if itemsOnPage < m.paginator().PerPage {
		n := (m.paginator().PerPage - itemsOnPage) * stashViewItemHeight
		for i := 0; i < n; i++ {
			fmt.Fprint(&b, "\n")
		}
	}

	return b.String()
}

// COMMANDS

func loadLocalMarkdown(md *markdown) tea.Cmd {
	return func() tea.Msg {
		if md.localPath == "" {
			return errMsg{errors.New("could not load file: missing path")}
		}

		data, err := os.ReadFile(md.localPath)
		if err != nil {
			log.Debug("error reading local file", "error", err)
			return errMsg{err}
		}
		md.Body = string(data)
		return fetchedMarkdownMsg(md)
	}
}

func filterMarkdowns(m stashModel) tea.Cmd {
	return func() tea.Msg {
		if m.filterInput.Value() == "" || !m.filterApplied() {
			return filteredMarkdownMsg(m.markdowns) // return everything
		}

		targets := []string{}
		mds := m.markdowns

		for _, t := range mds {
			targets = append(targets, t.filterValue)
		}

		ranks := fuzzy.Find(m.filterInput.Value(), targets)
		sort.Stable(ranks)

		filtered := []*markdown{}
		for _, r := range ranks {
			filtered = append(filtered, mds[r.Index])
		}

		return filteredMarkdownMsg(filtered)
	}
}
