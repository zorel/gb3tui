package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"hash/fnv"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

type Post struct {
	ID      int64  `json:"id"`
	Time    string `json:"time"`
	Info    string `json:"info"`
	Login   string `json:"login"`
	Message string `json:"message"`
	Tribune string `json:"tribune"`
}

type (
	postsMessage []Post
	sentMessage  struct{ err error }
	streamError  struct{ err error }
)

type postStream struct {
	posts  chan Post
	errors chan error
}

type model struct {
	client        *http.Client
	baseURL       string
	info          string
	tribunes      []string
	active        int
	blocked       map[string]bool
	gray          bool
	posts         map[string]Post
	input         textinput.Model
	scroll        int
	width, height int
	status        string
	stream        *postStream
	selectedKey   string
	clockIndex    int
	view          viewState
}

type conversationGraph struct {
	parents  map[string][]string
	children map[string][]string
}

type conversationSpan struct {
	root, firstKey, lastKey string
	start, end, lane        int
	singleMarker            string
}

type conversationLanes struct {
	spans     []conversationSpan
	byRoot    map[string]conversationSpan
	rootByKey map[string]string
	count     int
}

type postBlock struct {
	post      Post
	lines     []string
	startLine int
}

// parsedClockReference is a clock token parsed from a post message.
type parsedClockReference struct {
	raw     string
	time    string // HH:MM:SS as captured by the regexp
	tribune string // target tribune, including any @tribune suffix
}

// postIndex caches the clock references and lookups derived from a snapshot of
// the posts so rendering does not rescan every post for every reference.
type postIndex struct {
	all           []Post
	byKey         map[string]Post
	byTribuneTime map[string]map[string]Post
	references    map[string][]parsedClockReference
	links         map[string][]clockLink
}

// viewState is the expensive part of rendering, recomputed only when the model
// actually changes instead of on every View() call.
type viewState struct {
	index       postIndex
	visible     []Post
	graph       conversationGraph
	lanes       conversationLanes
	selected    int
	highlighted Post
	blocks      []postBlock
	lines       []string
	totalLines  int
	lineByKey   map[string]int
}

type inlineFormat struct {
	bold, italic, underline, strike bool
}

var (
	clockReference  = regexp.MustCompile(`(?:(?:[0-9]{4}-[0-9]{2}-[0-9]{2}[T# ]|[0-9]{2}/[0-9]{2}#)?([0-9]{2}:[0-9]{2}:[0-9]{2})(?:@([a-z]+))?)`)
	anchorURL       = regexp.MustCompile(`(?is)<a\s+[^>]*href=["']([^"']+)["'][^>]*>.*?</a>`)
	plainURL        = regexp.MustCompile(`https?://[^\s<>"']+`)
	inlineToken     = regexp.MustCompile(`https?://[^\s<>"']+|(?:(?:[0-9]{4}-[0-9]{2}-[0-9]{2}[T# ]|[0-9]{2}/[0-9]{2}#)?[0-9]{2}:[0-9]{2}:[0-9]{2}(?:@[a-z]+)?)`)
	browserVersions = []struct {
		name       string
		expression *regexp.Regexp
	}{
		{"Edge", regexp.MustCompile(`(?:Edg|EdgiOS|EdgA)/([0-9]+)`)},
		{"Opera", regexp.MustCompile(`(?:OPR|Opera)/([0-9]+)`)},
		{"Firefox", regexp.MustCompile(`(?:Firefox|FxiOS)/([0-9]+)`)},
		{"Chrome", regexp.MustCompile(`(?:Chrome|CriOS)/([0-9]+)`)},
		{"Safari", regexp.MustCompile(`Version/([0-9]+)(?:\.[0-9]+)*.*Safari/`)},
	}
)

var (
	titleStyle          = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#7D56F4"))
	mutedStyle          = lipgloss.NewStyle().Foreground(lipgloss.Color("#626262"))
	clockStyle          = lipgloss.NewStyle().Foreground(lipgloss.Color("#00BFA5"))
	urlStyle            = lipgloss.NewStyle().Foreground(lipgloss.Color("#5FAFFF")).Underline(true)
	nameStyle           = lipgloss.NewStyle().Foreground(lipgloss.Color("#FF9D00")).Bold(true)
	grayStyle           = lipgloss.NewStyle().Foreground(lipgloss.Color("#626262"))
	footerStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("#9A9A9A"))
	tribuneStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("#9A9A9A")).Padding(0, 1)
	activeTribuneStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FFFFFF")).Background(lipgloss.Color("#5A3EC8")).Padding(0, 1)
	selectionStyle      = lipgloss.NewStyle().Background(lipgloss.Color("#2E2E46"))
	clockHighlightStyle = lipgloss.NewStyle().Background(lipgloss.Color("#5A3EC8")).Foreground(lipgloss.Color("#FFFFFF")).Bold(true)
	threadColors        = []lipgloss.Color{
		"#FF5F87", "#5FAFFF", "#5FD787", "#FFD75F",
		"#AF87FF", "#FF875F", "#5FFFFF", "#D7AF5F",
	}
)

func main() {
	baseURL := flag.String("url", "http://localhost:16666", "gb2c base URL")
	info := flag.String("info", defaultInfo(), "nickname or user-agent sent with posts")
	tribuneFlag := flag.String("tribunes", "", "comma-separated tribunes; empty means all")
	blockedFlag := flag.String("blocked", "", "comma-separated login/user-agent identities to hide")
	gray := flag.Bool("gray", false, "show filtered posts as gray rows with hidden messages")
	flag.Parse()

	client := newHTTPClient()
	tribunes := splitList(*tribuneFlag)
	if len(tribunes) == 0 {
		var err error
		tribunes, err = fetchTribunes(client, *baseURL)
		if err != nil {
			fatal(err)
		}
	}
	if len(tribunes) == 0 {
		fatal(errors.New("no tribune available"))
	}
	blockedIdentities := identitySet(*blockedFlag)
	for _, identity := range fetchBlockedIdentities(client, *baseURL) {
		blockedIdentities[identity] = true
	}

	input := textinput.New()
	input.Prompt = "  » "
	input.Placeholder = "write a post and press Enter"
	input.CharLimit = 65536
	input.Focus()

	stream := newPostStream(client, *baseURL, tribunes)
	m := model{
		client: client, baseURL: strings.TrimRight(*baseURL, "/"), info: *info,
		tribunes: tribunes, blocked: blockedIdentities, gray: *gray,
		posts: make(map[string]Post), input: input, stream: stream,
		status: "connected",
	}
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := p.Run(); err != nil {
		fatal(err)
	}
}

func newHTTPClient() *http.Client {
	// No Client.Timeout: the poll endpoint is a long-lived SSE stream. Timeouts
	// are applied to connection setup and response headers instead.
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConns:          8,
		MaxIdleConnsPerHost:   4,
	}
	return &http.Client{Transport: transport}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(textinput.Blink, m.waitForStream())
}

func (m model) waitForStream() tea.Cmd {
	return func() tea.Msg {
		select {
		case post := <-m.stream.posts:
			// Drain everything already buffered so a burst of stored posts is
			// applied and rendered in one batch instead of once per message.
			posts := []Post{post}
			for {
				select {
				case next := <-m.stream.posts:
					posts = append(posts, next)
				default:
					return postsMessage(posts)
				}
			}
		case err := <-m.stream.errors:
			return streamError{err: err}
		}
	}
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	dirty := false
	reveal := false
	switch message := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = message.Width, message.Height
		m.input.Width = max(1, message.Width-4)
		dirty = true
	case postsMessage:
		for _, post := range message {
			m.posts[postKey(post)] = post
		}
		dirty = true
		cmd = m.waitForStream()
	case streamError:
		m.status = "reconnecting: " + message.err.Error()
		cmd = m.waitForStream()
	case sentMessage:
		if message.err != nil {
			m.status = "post failed: " + message.err.Error()
		} else {
			m.status = "posted to " + m.tribunes[m.active]
		}
	case tea.KeyMsg:
		switch message.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "q":
			if m.input.Value() == "" {
				return m, tea.Quit
			}
			m.input, cmd = m.input.Update(msg)
		case "tab":
			m.active = (m.active + 1) % len(m.tribunes)
			m.scroll = 0
			m.clockIndex = -1
			dirty = true
		case "shift+tab":
			m.active = (m.active + len(m.tribunes) - 1) % len(m.tribunes)
			m.scroll = 0
			m.clockIndex = -1
			dirty = true
		case "up":
			m.moveSelection(-1)
			dirty = true
			reveal = true
		case "down":
			m.moveSelection(1)
			dirty = true
			reveal = true
		case "left":
			m.stepClock(-1)
			dirty = true
			reveal = true
		case "right":
			m.stepClock(1)
			dirty = true
			reveal = true
		case "pgup", "ctrl+u":
			m.scroll += 4
		case "pgdown", "ctrl+d":
			m.scroll -= 4
		case "enter":
			if m.input.Value() == "" {
				if m.clockIndex >= 0 {
					m.jumpToSelectedClock()
					dirty = true
					reveal = true
				} else {
					posts := m.view.visible
					index := m.view.selected
					if index >= 0 && index < len(posts) {
						m.input.SetValue(clockForPost(posts[index]) + " ")
					}
				}
			} else {
				value := strings.TrimSpace(m.input.Value())
				if value != "" {
					m.input.Reset()
					cmd = sendPostCmd(m.client, m.baseURL, m.tribunes[m.active], m.info, value)
				}
			}
		default:
			m.input, cmd = m.input.Update(msg)
		}
	}
	if m.scroll < 0 {
		m.scroll = 0
	}
	// Rendering needs a width, so wait for the first WindowSizeMsg before
	// building the view state.
	if dirty && m.width > 0 {
		m.rebuildView()
	}
	if reveal {
		m.ensurePostVisible(m.view.highlighted)
	}
	return m, cmd
}

func (m *model) rebuildView() {
	all := make([]Post, 0, len(m.posts))
	for _, post := range m.posts {
		all = append(all, post)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].ID < all[j].ID })

	index := newPostIndex(all)
	hidden := m.hiddenPosts(index)
	visible := m.visiblePostsFrom(index, hidden)
	graph := buildConversationGraphWithIndex(all, index)
	lanes := buildConversationLanes(visible, graph)
	selected := m.selectedIndexFrom(index, visible)
	blocks, lines, totalLines, lineByKey := m.buildPostBlocks(visible, index, lanes, selected)

	var highlighted Post
	if selected >= 0 && selected < len(visible) {
		highlighted = visible[selected]
	}
	m.view = viewState{
		index:       index,
		visible:     visible,
		graph:       graph,
		lanes:       lanes,
		selected:    selected,
		highlighted: highlighted,
		blocks:      blocks,
		lines:       lines,
		totalLines:  totalLines,
		lineByKey:   lineByKey,
	}
}

func (m model) View() string {
	if m.width == 0 {
		return "connecting..."
	}
	state := m.view
	posts := state.visible
	selected := state.selected
	totalLines := state.totalLines

	contentHeight := m.contentHeight()
	maxScroll := totalLines - contentHeight
	if maxScroll < 0 {
		maxScroll = 0
	}
	scroll := m.scroll
	if scroll > maxScroll {
		scroll = maxScroll
	}
	if scroll < 0 {
		scroll = 0
	}
	start := totalLines - contentHeight - scroll
	if start < 0 {
		start = 0
	}
	end := start + contentHeight
	if end > totalLines {
		end = totalLines
	}

	body := make([]string, 0, contentHeight)
	if start < len(state.lines) {
		body = append(body, state.lines[start:end]...)
	}
	for len(body) < contentHeight {
		body = append(body, "")
	}

	selection := ""
	if selected >= 0 && selected < len(posts) {
		selection = fmt.Sprintf(" · post %d/%d", selected+1, len(posts))
	}
	if m.clockIndex >= 0 {
		if links, ok := state.index.links[m.selectedKey]; ok && m.clockIndex < len(links) {
			link := links[m.clockIndex]
			if link.found {
				selection += fmt.Sprintf(" · %s → %s", link.raw, displayIdentity(link.target))
			} else {
				selection += fmt.Sprintf(" · %s (no match)", link.raw)
			}
		}
	}
	header := titleStyle.Render(" gb3 ") + " " + mutedStyle.Render("/ "+m.baseURL)
	header += "   " + activeTribuneStyle.Render(m.tribunes[m.active])
	header += "   " + mutedStyle.Render(fmt.Sprintf("%d posts", len(posts)))
	header += mutedStyle.Render(selection)
	header = fitLine(header, m.width)
	tribuneBar := m.renderTribuneBar()
	inputLine := fitLine(m.input.View()+mutedStyle.Render("  "+m.status), m.width)
	footer := fitLine(footerStyle.Render("↑/↓ select  ←/→ clock  Tab tribune  PgUp/PgDn scroll  Enter send/follow  q quit"), m.width)
	return header + "\n" + tribuneBar + "\n" + strings.Join(body, "\n") + "\n" + inputLine + "\n" + footer
}

func (m model) hiddenPosts(index postIndex) map[string]bool {
	hidden := make(map[string]bool, len(index.all))
	queue := make([]string, 0)
	for _, post := range index.all {
		key := postKey(post)
		if m.blocked[post.Login] || m.blocked[post.Info] {
			hidden[key] = true
			queue = append(queue, key)
		}
	}
	if len(queue) == 0 {
		return hidden
	}

	// Propagate the hidden flag to every direct or indirect reply. Building the
	// reverse index once replaces the repeated full scans of the old fixed point.
	children := make(map[string][]string, len(index.all))
	for _, post := range index.all {
		key := postKey(post)
		for _, link := range index.links[key] {
			if !link.found {
				continue
			}
			targetKey := postKey(link.target)
			children[targetKey] = append(children[targetKey], key)
		}
	}
	for len(queue) > 0 {
		hiddenKey := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		for _, childKey := range children[hiddenKey] {
			if !hidden[childKey] {
				hidden[childKey] = true
				queue = append(queue, childKey)
			}
		}
	}
	return hidden
}

func (m model) visiblePostsFrom(index postIndex, hidden map[string]bool) []Post {
	visible := make([]Post, 0, len(index.all))
	activeTribune := m.tribunes[m.active]
	for _, post := range index.all {
		if post.Tribune != activeTribune {
			continue
		}
		key := postKey(post)
		if hidden[key] {
			if !m.gray {
				continue
			}
			post.Message = "[message hidden]"
		}
		visible = append(visible, post)
	}
	return visible
}

func (m model) contentHeight() int {
	height := m.height - 4 // header + tribune bar + input/status + footer
	if height < 1 {
		height = 1
	}
	return height
}

func fitLine(s string, width int) string {
	return lipgloss.NewStyle().MaxWidth(max(1, width)).Render(s)
}

func (m model) renderTribuneBar() string {
	names := make([]string, 0, len(m.tribunes))
	for index, tribune := range m.tribunes {
		if index == m.active {
			names = append(names, activeTribuneStyle.Render(tribune))
		} else {
			names = append(names, tribuneStyle.Render(tribune))
		}
	}
	return fitLine(strings.Join(names, " "), m.width)
}

func (m model) buildPostBlocks(posts []Post, index postIndex, lanes conversationLanes, selectedIndex int) ([]postBlock, []string, int, map[string]int) {
	rowWidth := max(1, m.width-2)
	var highlighted Post
	if selectedIndex >= 0 && selectedIndex < len(posts) {
		highlighted = posts[selectedIndex]
	}
	blocks := make([]postBlock, 0, len(posts))
	lineByKey := make(map[string]int, len(posts))
	lineOffset := 0
	for postIndexInList, post := range posts {
		lines := m.renderPostLines(post, postIndexInList, index, lanes, selectedIndex, highlighted, rowWidth)
		blocks = append(blocks, postBlock{post: post, lines: lines, startLine: lineOffset})
		lineByKey[postKey(post)] = lineOffset
		lineOffset += len(lines)
	}
	allLines := make([]string, 0, lineOffset)
	for _, block := range blocks {
		allLines = append(allLines, block.lines...)
	}
	return blocks, allLines, lineOffset, lineByKey
}

func (m model) renderPostLines(post Post, index int, postIndex postIndex, lanes conversationLanes, selectedIndex int, highlighted Post, rowWidth int) []string {
	gutter, laneStyle, inConversation := lanes.gutter(index, postKey(post))
	referenceStyle := clockStyle
	if inConversation {
		referenceStyle = laneStyle
	}

	isSelected := index == selectedIndex
	marker := "  "
	if isSelected {
		marker = "» "
	}
	clockText := referenceStyle.Render(post.Time[8:])
	if isSelected && m.clockIndex == -1 {
		clockText = clockHighlightStyle.Render(post.Time[8:])
	}
	name := displayIdentity(post)

	if m.gray && post.Message == "[message hidden]" {
		line := grayStyle.Render(fmt.Sprintf("%s%s%s  %s  %s", gutter, marker, post.Time[8:], name, post.Message))
		line = fitLine(line, rowWidth)
		return stylePostLines([]string{line}, isSelected, rowWidth)
	}

	messageText := styledMessageWithHighlights(post.Message, referenceStyle, referencingClockTokens(postIndex, post, highlighted))

	preName := fmt.Sprintf("%s%s%s  ", gutter, marker, clockText)
	prefix := preName + nameStyle.Render(name) + "  "
	preNameWidth := lipgloss.Width(preName)
	prefixWidth := lipgloss.Width(prefix)
	messageWidth := rowWidth - prefixWidth
	continuationWidth := rowWidth - preNameWidth
	if messageWidth < 1 {
		messageWidth = 1
	}
	if continuationWidth < 1 {
		continuationWidth = 1
	}

	wrappedLines := wrapWords(messageText, messageWidth, continuationWidth)
	continuation := strings.Repeat(" ", preNameWidth)

	lines := make([]string, 0, len(wrappedLines))
	for i, line := range wrappedLines {
		if i == 0 {
			lines = append(lines, prefix+line)
		} else {
			lines = append(lines, continuation+line)
		}
	}
	return stylePostLines(lines, isSelected, rowWidth)
}

func stylePostLines(lines []string, isSelected bool, rowWidth int) []string {
	for i, line := range lines {
		line = padLine(line, rowWidth)
		if isSelected {
			line = selectionStyle.Render(line)
		}
		lines[i] = line
	}
	return lines
}

func padLine(line string, width int) string {
	lineWidth := lipgloss.Width(line)
	if lineWidth >= width {
		return line
	}
	return line + strings.Repeat(" ", width-lineWidth)
}

func wrapWords(message string, firstWidth, contWidth int) []string {
	words := strings.Fields(message)
	if len(words) == 0 {
		return []string{""}
	}

	lines := make([]string, 0)
	var current strings.Builder
	currentWidth := 0
	limit := firstWidth

	for _, word := range words {
		wordWidth := lipgloss.Width(word)
		needed := wordWidth
		if currentWidth > 0 {
			needed++
		}
		if currentWidth > 0 && currentWidth+needed > limit {
			lines = append(lines, current.String())
			current.Reset()
			currentWidth = 0
			limit = contWidth
		}
		if currentWidth > 0 {
			current.WriteByte(' ')
			currentWidth++
		}
		current.WriteString(word)
		currentWidth += wordWidth
	}
	if currentWidth > 0 || len(lines) == 0 {
		lines = append(lines, current.String())
	}
	return lines
}

func buildConversationGraph(posts []Post) conversationGraph {
	return buildConversationGraphWithIndex(posts, newPostIndex(posts))
}

func buildConversationGraphWithIndex(posts []Post, index postIndex) conversationGraph {
	graph := conversationGraph{
		parents:  make(map[string][]string),
		children: make(map[string][]string),
	}
	for _, post := range posts {
		child := postKey(post)
		for _, link := range index.links[child] {
			if !link.found {
				continue
			}
			parent := postKey(link.target)
			if parent != "" && parent != child && !contains(graph.parents[child], parent) {
				graph.parents[child] = append(graph.parents[child], parent)
				graph.children[parent] = append(graph.children[parent], child)
			}
		}
	}
	return graph
}

func buildConversationLanes(posts []Post, graph conversationGraph) conversationLanes {
	lanes := conversationLanes{
		byRoot:    make(map[string]conversationSpan),
		rootByKey: make(map[string]string),
	}
	for index, post := range posts {
		key := postKey(post)
		if len(graph.parents[key]) == 0 && len(graph.children[key]) == 0 {
			continue
		}
		root := graph.root(key)
		lanes.rootByKey[key] = root
		span, exists := lanes.byRoot[root]
		if !exists {
			span = conversationSpan{root: root, firstKey: key, start: index, end: index}
		}
		span.lastKey = key
		span.end = index
		lanes.byRoot[root] = span
	}

	spans := make([]conversationSpan, 0, len(lanes.byRoot))
	for _, span := range lanes.byRoot {
		if span.start == span.end {
			span.singleMarker = "╷"
			if len(graph.parents[span.firstKey]) > 0 {
				span.singleMarker = "╵"
			}
		}
		spans = append(spans, span)
	}
	sort.Slice(spans, func(i, j int) bool {
		if spans[i].start == spans[j].start {
			return spans[i].root < spans[j].root
		}
		return spans[i].start < spans[j].start
	})
	laneEnds := make([]int, 0)
	for index := range spans {
		lane := -1
		for candidate, end := range laneEnds {
			if end < spans[index].start {
				lane = candidate
				break
			}
		}
		if lane < 0 {
			lane = len(laneEnds)
			laneEnds = append(laneEnds, spans[index].end)
		} else {
			laneEnds[lane] = spans[index].end
		}
		spans[index].lane = lane
		lanes.byRoot[spans[index].root] = spans[index]
	}
	lanes.spans = spans
	lanes.count = len(laneEnds)
	return lanes
}

func (lanes conversationLanes) gutter(row int, key string) (string, lipgloss.Style, bool) {
	root := lanes.rootByKey[key]
	own := lanes.byRoot[root]

	var gutter strings.Builder
	for lane := 0; lane < lanes.count; lane++ {
		marker := " "
		var active *conversationSpan
		for index := range lanes.spans {
			span := &lanes.spans[index]
			if span.lane == lane && row >= span.start && row <= span.end {
				active = span
				break
			}
		}
		if active != nil {
			marker = "│"
			if active.root == root {
				switch {
				case active.start == active.end:
					marker = active.singleMarker
				case row == active.start:
					marker = "┏"
				case row == active.end:
					marker = "┗"
				default:
					marker = "┣"
				}
			}
			gutter.WriteString(threadStyle(active.root).Render(marker))
		} else {
			gutter.WriteString(marker)
		}
	}
	gutter.WriteString(" ")
	if root == "" {
		return gutter.String(), lipgloss.Style{}, false
	}
	return gutter.String(), threadStyle(own.root), true
}

func (g conversationGraph) decoration(key string) (string, lipgloss.Style, bool) {
	if len(g.parents[key]) == 0 && len(g.children[key]) == 0 {
		return "", lipgloss.Style{}, false
	}
	marker := "┣"
	if len(g.parents[key]) == 0 {
		marker = "┏"
	} else if len(g.children[key]) == 0 {
		marker = "┗"
	}
	return marker, threadStyle(g.root(key)), true
}

func (g conversationGraph) root(key string) string {
	seen := make(map[string]bool)
	for len(g.parents[key]) > 0 && !seen[key] {
		seen[key] = true
		key = g.parents[key][0]
	}
	return key
}

func threadStyle(root string) lipgloss.Style {
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(root))
	color := threadColors[int(hash.Sum32())%len(threadColors)]
	return lipgloss.NewStyle().Foreground(color).Bold(true)
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

type clockLink struct {
	raw     string
	time    string
	tribune string
	target  Post
	found   bool
}

func newPostIndex(posts []Post) postIndex {
	index := postIndex{
		all:           posts,
		byKey:         make(map[string]Post, len(posts)),
		byTribuneTime: make(map[string]map[string]Post),
		references:    make(map[string][]parsedClockReference, len(posts)),
		links:         make(map[string][]clockLink, len(posts)),
	}
	for _, post := range posts {
		key := postKey(post)
		index.byKey[key] = post
		if len(post.Time) >= 14 {
			byTime := index.byTribuneTime[post.Tribune]
			if byTime == nil {
				byTime = make(map[string]Post)
				index.byTribuneTime[post.Tribune] = byTime
			}
			clock := post.Time[8:]
			current, exists := byTime[clock]
			if !exists || post.ID > current.ID {
				byTime[clock] = post
			}
		}
		index.references[key] = parseClockReferences(post)
	}
	for _, post := range posts {
		key := postKey(post)
		index.links[key] = index.resolveReferences(index.references[key])
	}
	return index
}

func parseClockReferences(post Post) []parsedClockReference {
	matches := clockReference.FindAllStringSubmatch(post.Message, -1)
	references := make([]parsedClockReference, 0, len(matches))
	for _, match := range matches {
		tribune := post.Tribune
		if match[2] != "" {
			tribune = match[2]
		}
		references = append(references, parsedClockReference{raw: match[0], time: match[1], tribune: tribune})
	}
	return references
}

func (index postIndex) findByClock(tribune, timeWithoutColons string) (Post, bool) {
	byTime := index.byTribuneTime[tribune]
	if byTime == nil {
		return Post{}, false
	}
	post, found := byTime[timeWithoutColons]
	return post, found
}

func (index postIndex) resolveReferences(references []parsedClockReference) []clockLink {
	links := make([]clockLink, 0, len(references))
	for _, reference := range references {
		target, found := index.findByClock(reference.tribune, strings.ReplaceAll(reference.time, ":", ""))
		links = append(links, clockLink{
			raw:     reference.raw,
			time:    reference.time,
			tribune: reference.tribune,
			target:  target,
			found:   found,
		})
	}
	return links
}

func findPostByClock(posts []Post, tribune, timeWithoutColons string) (Post, bool) {
	return newPostIndex(posts).findByClock(tribune, timeWithoutColons)
}

func postClockLinks(posts []Post, post Post) []clockLink {
	return newPostIndex(posts).resolveReferences(parseClockReferences(post))
}

func referencingClockTokens(index postIndex, post Post, target Post) map[string]bool {
	links, ok := index.links[postKey(post)]
	if !ok {
		links = index.resolveReferences(parseClockReferences(post))
	}
	targetKey := postKey(target)
	tokens := make(map[string]bool)
	for _, link := range links {
		if link.found && postKey(link.target) == targetKey {
			tokens[link.raw] = true
		}
	}
	if len(tokens) == 0 {
		return nil
	}
	return tokens
}

func (m model) highlightedPostFrom(index postIndex, visible []Post) (Post, bool) {
	if anchor, ok := index.byKey[m.selectedKey]; ok {
		if m.clockIndex >= 0 {
			links := index.links[m.selectedKey]
			if m.clockIndex < len(links) && links[m.clockIndex].found {
				return links[m.clockIndex].target, true
			}
		}
		return anchor, true
	}
	if len(visible) > 0 {
		return visible[len(visible)-1], true
	}
	return Post{}, false
}

func (m model) selectedIndexFrom(index postIndex, visible []Post) int {
	if highlighted, ok := m.highlightedPostFrom(index, visible); ok {
		highlightedKey := postKey(highlighted)
		for position, post := range visible {
			if postKey(post) == highlightedKey {
				return position
			}
		}
	}
	if m.selectedKey != "" {
		for position, post := range visible {
			if postKey(post) == m.selectedKey {
				return position
			}
		}
	}
	if len(visible) > 0 {
		return len(visible) - 1
	}
	return -1
}

func (m *model) activateTribune(tribune string) {
	if tribune == m.tribunes[m.active] {
		return
	}
	for index, candidate := range m.tribunes {
		if candidate == tribune {
			m.active = index
			m.scroll = 0
			return
		}
	}
}

func (m *model) moveSelection(delta int) {
	posts := m.view.visible
	if len(posts) == 0 {
		m.selectedKey = ""
		m.clockIndex = -1
		return
	}
	index := m.view.selected + delta
	if index < 0 {
		index = 0
	}
	if index >= len(posts) {
		index = len(posts) - 1
	}
	m.selectedKey = postKey(posts[index])
	m.clockIndex = -1
}

func (m *model) stepClock(delta int) {
	index := m.view.index
	anchor, ok := index.byKey[m.selectedKey]
	if !ok {
		posts := m.view.visible
		if len(posts) == 0 {
			m.selectedKey = ""
			m.clockIndex = -1
			return
		}
		anchor = posts[len(posts)-1]
		m.selectedKey = postKey(anchor)
	}
	links := index.links[postKey(anchor)]
	count := len(links) + 1
	position := m.clockIndex + 1
	position = (position + delta + count) % count
	m.clockIndex = position - 1

	highlighted := anchor
	if m.clockIndex >= 0 && m.clockIndex < len(links) && links[m.clockIndex].found {
		highlighted = links[m.clockIndex].target
	}
	m.activateTribune(highlighted.Tribune)
}

func (m *model) jumpToSelectedClock() {
	if m.clockIndex < 0 {
		return
	}
	index := m.view.index
	anchor, ok := index.byKey[m.selectedKey]
	if !ok {
		return
	}
	links := index.links[postKey(anchor)]
	if m.clockIndex >= len(links) || !links[m.clockIndex].found {
		return
	}
	target := links[m.clockIndex].target
	m.selectedKey = postKey(target)
	m.clockIndex = -1
	m.activateTribune(target.Tribune)
}

func (m *model) ensurePostVisible(post Post) {
	if len(m.view.visible) == 0 {
		return
	}
	targetLine, found := m.view.lineByKey[postKey(post)]
	if !found {
		targetLine = 0
	}
	totalLines := m.view.totalLines

	contentHeight := m.contentHeight()
	maxScroll := totalLines - contentHeight
	if maxScroll < 0 {
		maxScroll = 0
	}
	start := totalLines - contentHeight - m.scroll
	end := start + contentHeight - 1
	if targetLine < start {
		m.scroll = totalLines - contentHeight - targetLine
	} else if targetLine > end {
		m.scroll = totalLines - targetLine - 1
	}
	if m.scroll < 0 {
		m.scroll = 0
	}
	if m.scroll > maxScroll {
		m.scroll = maxScroll
	}
}

func newPostStream(client *http.Client, baseURL string, tribunes []string) *postStream {
	stream := &postStream{posts: make(chan Post, 64), errors: make(chan error, 1)}
	go func() {
		query := url.Values{}
		query.Set("filter", strings.Join(tribunes, ","))
		endpoint := strings.TrimRight(baseURL, "/") + "/gb2c/poll?" + query.Encode()
		for {
			response, err := client.Get(endpoint)
			if err != nil {
				stream.errors <- err
				time.Sleep(time.Second)
				continue
			}
			err = readEvents(bufio.NewReader(response.Body), stream.posts)
			response.Body.Close()
			if err != nil {
				stream.errors <- err
			}
			time.Sleep(time.Second)
		}
	}()
	return stream
}

func readEvents(reader *bufio.Reader, output chan<- Post) error {
	var data strings.Builder
	for {
		line, err := reader.ReadString('\n')
		if err != nil && len(line) == 0 {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		line = strings.TrimRight(line, "\r\n")
		if strings.HasPrefix(line, "data:") {
			data.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		} else if line == "" && data.Len() > 0 {
			var post Post
			if json.Unmarshal([]byte(data.String()), &post) == nil {
				output <- post
			}
			data.Reset()
		}
		if err != nil {
			return err
		}
	}
}

func sendPostCmd(client *http.Client, baseURL, tribune, info, message string) tea.Cmd {
	return func() tea.Msg {
		data := url.Values{"tribune": {tribune}, "info": {info}, "message": {message}}
		request, err := http.NewRequest(http.MethodPost, strings.TrimRight(baseURL, "/")+"/gb2c/post", strings.NewReader(data.Encode()))
		if err == nil {
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			var response *http.Response
			response, err = client.Do(request)
			if err == nil {
				defer response.Body.Close()
				if response.StatusCode >= 300 {
					err = fmt.Errorf("server returned %s", response.Status)
				}
			}
		}
		return sentMessage{err: err}
	}
}

func fetchTribunes(client *http.Client, baseURL string) ([]string, error) {
	response, err := client.Get(strings.TrimRight(baseURL, "/") + "/gb2c/list")
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tribune list returned %s", response.Status)
	}
	var tribunes []string
	if err := json.NewDecoder(response.Body).Decode(&tribunes); err != nil {
		return nil, err
	}
	sort.Strings(tribunes)
	return tribunes, nil
}

func fetchBlockedIdentities(client *http.Client, baseURL string) []string {
	response, err := client.Get(strings.TrimRight(baseURL, "/") + "/gb2c/config")
	if err != nil {
		return nil
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil
	}
	var config struct {
		BlockedIdentities []string `json:"blockedIdentities"`
	}
	if json.NewDecoder(response.Body).Decode(&config) != nil {
		return nil
	}
	return config.BlockedIdentities
}

func postKey(post Post) string { return post.Tribune + ":" + strconv.FormatInt(post.ID, 10) }

func clockForPost(post Post) string {
	if len(post.Time) < 14 {
		return post.Time
	}
	return post.Time[8:10] + ":" + post.Time[10:12] + ":" + post.Time[12:14]
}

func displayIdentity(post Post) string {
	if post.Login != "" {
		return post.Login
	}
	return shortBrowserUserAgent(post.Info)
}

func shortBrowserUserAgent(userAgent string) string {
	for _, browser := range browserVersions {
		matches := browser.expression.FindStringSubmatch(userAgent)
		if len(matches) < 2 {
			continue
		}
		identity := browser.name + " " + matches[1]
		if platform := browserPlatform(userAgent); platform != "" {
			identity += " · " + platform
		}
		return identity
	}
	return userAgent
}

func browserPlatform(userAgent string) string {
	switch {
	case strings.Contains(userAgent, "Android"):
		return "Android"
	case strings.Contains(userAgent, "iPad"):
		return "iPadOS"
	case strings.Contains(userAgent, "iPhone"), strings.Contains(userAgent, "iPod"):
		return "iOS"
	case strings.Contains(userAgent, "Windows"):
		return "Windows"
	case strings.Contains(userAgent, "CrOS"):
		return "ChromeOS"
	case strings.Contains(userAgent, "Macintosh"), strings.Contains(userAgent, "Mac OS X"):
		return "macOS"
	case strings.Contains(userAgent, "Linux"):
		return "Linux"
	default:
		return ""
	}
}

func defaultInfo() string {
	if value := os.Getenv("GB3_TUI_INFO"); value != "" {
		return value
	}
	if value := os.Getenv("USER"); value != "" {
		return value
	}
	return "gb3-tui"
}

func splitList(value string) []string {
	var result []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			result = append(result, item)
		}
	}
	return result
}

func identitySet(value string) map[string]bool {
	result := make(map[string]bool)
	for _, item := range splitList(value) {
		result[item] = true
	}
	return result
}

func clean(value string) string {
	return strings.Map(func(character rune) rune {
		if character < 32 || character == 127 {
			return -1
		}
		return character
	}, strings.ReplaceAll(strings.ReplaceAll(value, "\r", " "), "\n", " "))
}

func prettyMessage(value string) string {
	value = anchorURL.ReplaceAllStringFunc(value, func(anchor string) string {
		matches := anchorURL.FindStringSubmatch(anchor)
		return urlLabel(matches[1])
	})
	return plainURL.ReplaceAllStringFunc(value, urlLabel)
}

func styledMessage(value string) string {
	return styledMessageWithClockStyle(value, clockStyle)
}

func styledMessageWithClockStyle(value string, referenceStyle lipgloss.Style) string {
	return styledMessageWithHighlights(value, referenceStyle, nil)
}

func styledMessageWithHighlights(value string, referenceStyle lipgloss.Style, highlights map[string]bool) string {
	context := &html.Node{Type: html.ElementNode, Data: "div", DataAtom: atom.Div}
	nodes, err := html.ParseFragment(strings.NewReader(value), context)
	if err != nil {
		return renderStyledText(value, inlineFormat{}, referenceStyle, highlights)
	}
	var rendered strings.Builder
	for _, node := range nodes {
		rendered.WriteString(renderHTMLNode(node, inlineFormat{}, referenceStyle, highlights))
	}
	return rendered.String()
}

func renderHTMLNode(node *html.Node, format inlineFormat, referenceStyle lipgloss.Style, highlights map[string]bool) string {
	if node.Type == html.TextNode {
		return renderStyledText(node.Data, format, referenceStyle, highlights)
	}
	if node.Type != html.ElementNode {
		return ""
	}

	switch strings.ToLower(node.Data) {
	case "b", "strong":
		format.bold = true
	case "i", "em":
		format.italic = true
	case "u":
		format.underline = true
	case "s", "strike", "del":
		format.strike = true
	case "a":
		for _, attribute := range node.Attr {
			if strings.EqualFold(attribute.Key, "href") {
				style := format.style().Foreground(urlStyle.GetForeground()).Underline(true)
				return style.Render(urlLabel(attribute.Val))
			}
		}
	case "br":
		return " "
	case "script", "style":
		return ""
	}

	var rendered strings.Builder
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		rendered.WriteString(renderHTMLNode(child, format, referenceStyle, highlights))
	}
	return rendered.String()
}

func renderStyledText(value string, format inlineFormat, referenceStyle lipgloss.Style, highlights map[string]bool) string {
	value = clean(value)
	indices := inlineToken.FindAllStringIndex(value, -1)
	if len(indices) == 0 {
		return format.style().Render(value)
	}
	var rendered strings.Builder
	position := 0
	for _, index := range indices {
		rendered.WriteString(format.style().Render(value[position:index[0]]))
		token := value[index[0]:index[1]]
		style := format.style()
		isHighlight := highlights != nil && highlights[token]
		if plainURL.MatchString(token) {
			style = style.Foreground(urlStyle.GetForeground()).Underline(true)
			token = urlLabel(token)
		} else {
			style = style.Foreground(referenceStyle.GetForeground()).Bold(true)
		}
		if isHighlight {
			style = clockHighlightStyle
		}
		rendered.WriteString(style.Render(token))
		position = index[1]
	}
	rendered.WriteString(format.style().Render(value[position:]))
	return rendered.String()
}

func (format inlineFormat) style() lipgloss.Style {
	return lipgloss.NewStyle().
		Bold(format.bold).
		Italic(format.italic).
		Underline(format.underline).
		Strikethrough(format.strike)
}

func urlLabel(value string) string {
	core := strings.TrimRight(value, ".,!?;:)]")
	suffix := value[len(core):]
	parsed, err := url.Parse(core)
	if err != nil || parsed.Hostname() == "" {
		return value
	}
	return parsed.Scheme + "://" + parsed.Host + suffix
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
