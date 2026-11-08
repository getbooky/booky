--[[
Booky read-along: plays a book's audiobook on the e-reader and follows it in
the text — the sentence being read is highlighted and pages turn with it.

Booky does the hard parts on the server: it lines the narration up with the
book, makes a read-along edition whose sentences carry ids, and decodes the
audio to raw PCM (s16le, mono). Here we download those, play the PCM, and
use the timing to find each sentence on the page.

Sound on a Kindle: there is no speaker and no ALSA; audio goes
GStreamer -> mixersink -> audiomgrd -> Bluetooth. The pipeline that works is
    fdsrc ! raw caps ! mixersink stream-type=Music sync=true
with audiomgrd's focus set to Music first (sync=true matters: without it
mixersink rejects every buffer). Firmware ships gst-launch-0.10 or, newer,
gst-launch-1.0. Bluetooth headphones must be paired in the Kindle's own
settings. While paused, a stream of silence keeps the Bluetooth link from
going to sleep, which otherwise leaves the next play silent.

Elsewhere (Linux desktop, the KOReader emulator) aplay or paplay play the
same PCM; with neither, playback runs on the clock alone, silently, which is
enough to follow along in testing.

Finding a sentence: KOReader's engine merges an EPUB's chapters into one
document and renames every id to "_doc_fragment_<n>_ <id>"; Booky sends n
with each sentence. Each sentence is a <span> holding one text node, so the
range to highlight runs from its first character to its last.
]]

local M = {}

-- State shared by every instance of the plugin: the file browser and the
-- open book each load their own, but there is one set of downloads and one
-- player. Downloads in flight, by book; a generation per book that bumps
-- when its audio is freed, so a download already running stops and doesn't
-- bring it back; and the player now following a book, if any.
local shared = { fetching = {}, gen = {}, player = nil, keepalive = nil }

-- ---------------------------------------------------------------------
-- Pure helpers (no KOReader): exported for the tests.
-- ---------------------------------------------------------------------

local U = {}
M.util = U

-- unitAt returns the index of the last unit that has begun by t, or 0.
function U.unitAt(units, t)
    local lo, hi, at = 1, #units, 0
    while lo <= hi do
        local mid = math.floor((lo + hi) / 2)
        if units[mid].begin <= t + 0.05 then
            at = mid
            lo = mid + 1
        else
            hi = mid - 1
        end
    end
    return at
end

-- chunkAt returns the index of the chunk that plays at t.
function U.chunkAt(chunks, t)
    local i = #chunks
    while i > 1 and chunks[i].start > t do i = i - 1 end
    return math.max(i, 1)
end

-- crengineId is the id KOReader's engine gives a sentence.
function U.crengineId(frag, id)
    return "_doc_fragment_" .. tostring(frag) .. "_ " .. id
end

-- utf8len counts characters, which is how the engine counts text offsets.
function U.utf8len(s)
    local _, n = s:gsub("[^\128-\191]", "")
    return n
end

-- shellQuote makes s one single-quoted shell word.
function U.shellQuote(s)
    return "'" .. tostring(s):gsub("'", "'\\''") .. "'"
end

-- pcmOffset is the byte offset of time t (seconds) into a chunk that
-- starts at start, frame-aligned so playback never begins mid-sample.
function U.pcmOffset(t, start, rate)
    local frames = math.floor(math.max(0, t - start) * rate)
    return frames * 2
end

-- feedCommand is the shell command that writes the PCM from segment 1
-- (from its byte offset) through the last, back to back.
function U.feedCommand(segments)
    local parts = {}
    for i, seg in ipairs(segments) do
        if i == 1 and seg.skip > 0 then
            table.insert(parts, "tail -c +" .. tostring(seg.skip + 1) .. " " .. U.shellQuote(seg.path))
        else
            table.insert(parts, "cat " .. U.shellQuote(seg.path))
        end
    end
    return "{ " .. table.concat(parts, "; ") .. "; }"
end

-- sinkCommand is the player that reads PCM from stdin. The GStreamer ones
-- run with -e, so asked to stop (SIGINT) they finish the stream and let go
-- of the Kindle's audio service cleanly, and without -q, so the log shows
-- what the pipeline did.
function U.sinkCommand(backend, rate)
    rate = tostring(rate)
    if backend == "gst010" then
        return "gst-launch-0.10 -e fdsrc fd=0 name=bookyra"
            .. " ! audio/x-raw-int,endianness=1234,signed=true,width=16,depth=16,rate=" .. rate .. ",channels=1"
            .. " ! mixersink stream-type=Music sync=true"
    elseif backend == "gst1" then
        return "gst-launch-1.0 -e fdsrc fd=0 name=bookyra do-timestamp=true"
            .. " ! audio/x-raw,format=S16LE,rate=" .. rate .. ",channels=1,layout=interleaved"
            .. " ! mixersink stream-type=Music sync=true"
    elseif backend == "aplay" then
        return "aplay -q -t raw -f S16_LE -r " .. rate .. " -c 1 -"
    elseif backend == "paplay" then
        return "paplay --raw --format=s16le --rate=" .. rate .. " --channels=1"
    end
    return nil
end

-- Speeds: 0.5× to 2×, in tenths.
U.MIN_SPEED, U.MAX_SPEED = 0.5, 2.0

-- clampSpeed rounds a speed to a tenth and keeps it in range.
function U.clampSpeed(x)
    x = tonumber(x) or 1
    x = math.floor(x * 10 + 0.5) / 10
    if x < U.MIN_SPEED then x = U.MIN_SPEED elseif x > U.MAX_SPEED then x = U.MAX_SPEED end
    return x
end

-- stretchCommand is the step that plays the PCM at speed without changing
-- its pitch, run by KOReader's LuaJIT; nil at normal speed.
function U.stretchCommand(luajit, script, speed, rate)
    if not luajit or not script or speed == 1 then return nil end
    return U.shellQuote(luajit) .. " " .. U.shellQuote(script) .. " "
        .. string.format("%.1f", speed) .. " " .. tostring(math.floor(rate))
end

-- keepaliveCommand streams silence into the mixer so Bluetooth stays awake.
function U.keepaliveCommand(backend, rate)
    rate = tostring(rate)
    if backend == "gst010" then
        return "gst-launch-0.10 -q -e filesrc location=/dev/zero name=bookyka"
            .. " ! audio/x-raw-int,endianness=1234,signed=true,width=16,depth=16,rate=" .. rate .. ",channels=1"
            .. " ! mixersink stream-type=Music sync=true"
    elseif backend == "gst1" then
        return "gst-launch-1.0 -q -e filesrc location=/dev/zero name=bookyka"
            .. " ! audio/x-raw,format=S16LE,rate=" .. rate .. ",channels=1,layout=interleaved"
            .. " ! mixersink stream-type=Music sync=true"
    end
    return nil
end

-- strayKillCommand kills any player left behind: the content pipeline (its
-- gst-launch is named bookyra) and whatever is reading the chunk files. One
-- letter of each pattern is bracketed: the shell running this has the
-- patterns in its own command line, and an unbracketed one matches that
-- shell, killing it before the next command runs.
function U.strayKillCommand()
    return "pkill -9 -f '[n]ame=bookyra' >/dev/null 2>&1; pkill -9 -f '/[.]booky-readalong/' >/dev/null 2>&1;"
        .. " pkill -9 -f '[b]ooky_stretch[.]lua' >/dev/null 2>&1"
end

-- firstUnitFrom returns the first unit (by index) whose page is >= page,
-- given pageOf(i) — a binary search, since units run in reading order.
-- Returns nil when every unit is on an earlier page.
function U.firstUnitFrom(n, pageOf, page)
    local lo, hi, found = 1, n, nil
    while lo <= hi do
        local mid = math.floor((lo + hi) / 2)
        local p = pageOf(mid)
        if p and p >= page then
            found = mid
            hi = mid - 1
        else
            lo = mid + 1
        end
    end
    return found
end

-- nextWake is how long to sleep before the next sentence starts (or the
-- streamed audio runs out), clamped so the clock is checked now and then.
-- Positions are in the book's time; at speed, they pass speed times faster.
function U.nextWake(units, i, pos, streamEnd, speed)
    local nextAt = streamEnd
    if units[i + 1] and units[i + 1].begin < nextAt then nextAt = units[i + 1].begin end
    return math.min(5, math.max(0.05, (nextAt - pos) / (speed or 1)))
end

-- spillAt is when, in the book's time, a sentence's narration reaches the
-- words on the next page: its share of the sentence's characters (before
-- of total) into the time it's spoken.
function U.spillAt(begin, finish, before, total)
    local frac = 0.5
    if total and total > 0 and before then frac = before / total end
    if frac < 0 then frac = 0 elseif frac > 1 then frac = 1 end
    return begin + (finish - begin) * frac
end

-- elementXPointer cuts a reading position down to the element it's in:
-- "/body/DocFragment[5]/body/p[12]/text().34" becomes
-- "/body/DocFragment[5]/body/p[12]". The read-along copy wraps sentences
-- in spans inside paragraphs, so a position inside a paragraph's text
-- doesn't carry over — but the paragraph itself is at the same path.
function U.elementXPointer(xp)
    if type(xp) ~= "string" or xp:sub(1, 1) ~= "/" then return nil end
    xp = xp:gsub("/text%(%).*$", ""):gsub("%.%d+$", "")
    if xp == "" then return nil end
    return xp
end

-- formatSize writes a byte count for people: "740 MB", "1.2 GB".
function U.formatSize(bytes)
    if bytes >= 1024 ^ 3 then return string.format("%.1f GB", bytes / 1024 ^ 3) end
    if bytes >= 1024 ^ 2 then return string.format("%.0f MB", bytes / 1024 ^ 2) end
    return string.format("%.0f KB", math.max(bytes, 0) / 1024)
end

-- validManifest checks what the server sent before anything trusts it.
function U.validManifest(m)
    if type(m) ~= "table" or type(m.audio) ~= "table" or m.audio.format ~= "s16le"
        or type(m.audio.rate) ~= "number" or m.audio.rate <= 0 or m.audio.channels ~= 1
        or type(m.chunks) ~= "table" or #m.chunks == 0 or type(m.units) ~= "table" or #m.units == 0 then
        return false
    end
    for i, c in ipairs(m.chunks) do
        if type(c.n) ~= "number" or c.n ~= i or type(c.start) ~= "number" or type(c.duration) ~= "number" then
            return false
        end
    end
    for _, u in ipairs(m.units) do
        if type(u.id) ~= "string" or not u.id:match("^booky%-s%d+$") or type(u.frag) ~= "number"
            or type(u.begin) ~= "number" or type(u["end"]) ~= "number" then
            return false
        end
    end
    table.sort(m.units, function(a, b) return a.begin < b.begin end)
    return true
end

-- ---------------------------------------------------------------------
-- The device side. Everything below needs KOReader.
-- ---------------------------------------------------------------------

function M.new(plugin)
    local Device = require("device")
    local Event = require("ui/event")
    local Geom = require("ui/geometry")
    local InfoMessage = require("ui/widget/infomessage")
    local NetworkMgr = require("ui/network/manager")
    local UIManager = require("ui/uimanager")
    local ffi = require("ffi")
    local ffiutil = require("ffi/util")
    local json = require("json")
    local lfs = require("libs/libkoreader-lfs")
    local logger = require("logger")
    local time = require("ui/time")
    local _ = require("gettext")
    pcall(ffi.cdef, "int kill(int pid, int sig);")

    -- the playback log: in KOReader's own folder, so it outlasts a reboot
    -- (the Kindle empties /tmp), and kept small
    local LOG = "/tmp/booky-readalong.log"
    local okds, DataStorage = pcall(require, "datastorage")
    if okds and DataStorage and DataStorage.getDataDir then
        LOG = DataStorage:getDataDir() .. "/booky-readalong.log"
    end
    if (lfs.attributes(LOG, "size") or 0) > 256 * 1024 then
        os.rename(LOG, LOG .. ".old")
    end
    local RA = { plugin = plugin, settings = plugin.settings, LOG = LOG }

    -- note writes one timestamped line to the playback log.
    local function note(...)
        local f = io.open(LOG, "a")
        if not f then return end
        local parts = {}
        for i = 1, select("#", ...) do parts[i] = tostring((select(i, ...))) end
        f:write(os.date("%m-%d %H:%M:%S "), table.concat(parts, " "), "\n")
        f:close()
    end
    RA.note = note

    -- logTail returns the log's last n lines, newest last.
    function RA:logTail(n)
        local f = io.open(LOG, "rb")
        if not f then return "" end
        local size = f:seek("end")
        f:seek("set", math.max(0, size - 64 * 1024))
        local data = f:read("*a") or ""
        f:close()
        local lines = {}
        for line in data:gmatch("[^\n]+") do lines[#lines + 1] = line end
        local from = math.max(1, #lines - n + 1)
        return table.concat(lines, "\n", from)
    end

    local function toast(text, timeout)
        UIManager:show(InfoMessage:new{ text = text, timeout = timeout or 3 })
    end

    local function which(name)
        local h = io.popen("which " .. name .. " 2>/dev/null")
        if not h then return false end
        local out = h:read("*l")
        h:close()
        return out ~= nil and out ~= ""
    end

    local function killGroup(pid, sig)
        if not pid then return end
        pcall(function() ffi.C.kill(-pid, sig or 15) end)
        pcall(function() ffi.C.kill(pid, sig or 15) end)
    end

    local function alive(pid)
        if not pid then return false end
        local ok, r = pcall(function() return ffi.C.kill(pid, 0) end)
        return ok and r == 0
    end

    -- stopGroup asks a player's process group to stop (SIGINT: GStreamer
    -- run with -e finishes the stream and releases the audio service),
    -- waiting up to a second, and kills what's left.
    local function stopGroup(pid)
        if not pid or not alive(pid) then return end
        killGroup(pid, 2)
        for _i = 1, 10 do
            if not alive(pid) then return end
            os.execute("sleep 0.1")
        end
        note("player", pid, "didn't stop when asked; killing it")
        killGroup(pid, 9)
    end

    -- spawn runs cmd in its own process group, logging to LOG; returns pid.
    local has_setsid = which("setsid")
    local function spawn(cmd)
        local wrapped = "sh -c " .. U.shellQuote(cmd)
        if has_setsid then wrapped = "setsid " .. wrapped end
        local h = io.popen(wrapped .. " </dev/null >>" .. LOG .. " 2>&1 & echo $!")
        if not h then return nil end
        local pid = tonumber((h:read("*a") or ""):match("(%d+)"))
        h:close()
        return pid
    end

    -- ---- where things live ----

    -- the device's read-along files: <download dir>/.booky-readalong/<book>/
    function RA:dir(book_id)
        local root = plugin:downloadRoot()
        if not root then return nil end
        return root .. "/.booky-readalong/" .. tostring(book_id)
    end

    -- editionPath is where the read-along goes: the book's own file. The
    -- read-along edition is the whole book — it only adds sentence marks —
    -- so it takes the book's place instead of sitting beside it as a second
    -- copy; the audio is extra and plays only when asked.
    function RA:editionPath(book)
        return plugin:bookDest(book)
    end

    function RA:chunkPath(book_id, n)
        return self:dir(book_id) .. string.format("/%04d.pcm", n)
    end

    -- known maps read-along edition paths to {id, libraryId}
    function RA:known()
        return self.settings:readSetting("readalong_books") or {}
    end

    function RA:bookFor(path)
        return path and self:known()[path] or nil
    end

    -- ---- downloading ----

    -- fetchInChild downloads url to dest in a forked process, so a big
    -- chunk never freezes the screen; done(ok) runs when it finishes.
    function RA:fetchInChild(path, dest, done)
        local part = dest .. ".part"
        os.remove(part)
        local url = plugin.config.server_url .. path
        local token = plugin.config.token
        local pid = ffiutil.runInSubProcess(function()
            local http = require("socket.http")
            local ltn12 = require("ltn12")
            local socketutil = require("socketutil")
            local f = io.open(part, "wb")
            if not f then return end
            socketutil:set_timeout(30, 600)
            local ok, code = http.request{
                url = url,
                headers = { ["Authorization"] = "Bearer " .. token },
                sink = ltn12.sink.file(f),
            }
            if ok and code == 200 then
                os.rename(part, dest)
            else
                os.remove(part)
            end
        end)
        if not pid then
            done(false)
            return
        end
        local function poll()
            if ffiutil.isSubProcessDone(pid) then
                done(lfs.attributes(dest, "mode") == "file")
            else
                UIManager:scheduleIn(1, poll)
            end
        end
        UIManager:scheduleIn(1, poll)
    end

    function RA:manifestPath(book_id)
        return self:dir(book_id) .. "/manifest.json"
    end

    function RA:loadManifest(book_id)
        local f = io.open(self:manifestPath(book_id), "rb")
        if not f then return nil end
        local data = f:read("*a")
        f:close()
        local ok, m = pcall(json.decode, data)
        if ok and U.validManifest(m) then return m end
        return nil
    end

    function RA:apiPath(book)
        return "/api/koreader/v1/readalong/" .. tostring(book.libraryId) .. "/" .. tostring(book.id)
    end

    -- download gets the manifest and edition now (small), then the audio
    -- chunk by chunk in the background. done(edition_path) runs once the
    -- book can be opened; audio keeps arriving after. opts.closed: the
    -- caller closed the book in the reader just now (its plugin's ui may
    -- still name the file).
    function RA:download(book, done, opts)
        local dir = self:dir(book.id)
        if not dir then return end
        lfs.mkdir(plugin:downloadRoot() .. "/.booky-readalong")
        lfs.mkdir(dir)
        local body, err = plugin:request(self:apiPath(book))
        local ok, m = pcall(json.decode, body or "")
        if not body or not ok or not U.validManifest(m) then
            toast(_("Booky: couldn't get the read-along: ") .. tostring(err or _("bad response")))
            return
        end
        local edition = self:editionPath(book)
        -- the file can't be swapped while it's open
        local open = plugin.ui and plugin.ui.document and plugin.ui.document.file
        if open == edition and not (opts and opts.closed) then
            toast(_("Booky: close the book first, then add its read-along."), 5)
            return
        end
        local old = self:loadManifest(book.id)
        local changed = not old or type(old.edition) ~= "table" or type(m.edition) ~= "table"
            or old.edition.modified ~= m.edition.modified
        if changed then
            -- rebuilt on the server: the old audio doesn't match the new timing
            for name in lfs.dir(dir) do
                if name:match("^%d%d%d%d%.pcm") then os.remove(dir .. "/" .. name) end
            end
        end
        local is_readalong = self:bookFor(edition) ~= nil and lfs.attributes(edition, "mode") == "file"
        if changed or not is_readalong then
            if not plugin:request(self:apiPath(book) .. "/edition", edition .. ".part") then
                os.remove(edition .. ".part")
                toast(_("Booky: couldn't download the read-along book"))
                return
            end
            -- replacing the plain book: keep the reader's place
            local place = lfs.attributes(edition, "mode") == "file" and self:readPlace(edition)
            os.rename(edition .. ".part", edition)
            self:keepPlace(edition, place)
        end
        local f = io.open(self:manifestPath(book.id), "wb")
        if f then
            f:write(body)
            f:close()
        end
        local known = self:known()
        known[edition] = { id = book.id, libraryId = book.libraryId, title = book.title }
        self.settings:saveSetting("readalong_books", known)
        self.settings:flush()
        self:fetchAudio(book.id)
        if done then done(edition) end
    end

    -- fetchAudio downloads missing chunks one after another, starting from
    -- `from` (the one needed soonest), then wrapping round. Returns whether
    -- a download is running now.
    function RA:fetchAudio(book_id, from)
        local m = self:loadManifest(book_id)
        local info
        for _p, b in pairs(self:known()) do
            if b.id == book_id then info = b end
        end
        if not m or not info then return false end
        if shared.fetching[book_id] then return true end
        local order = {}
        from = from or 1
        for k = 0, #m.chunks - 1 do
            local n = (from - 1 + k) % #m.chunks + 1
            if lfs.attributes(self:chunkPath(book_id, n), "mode") ~= "file" then table.insert(order, n) end
        end
        if #order == 0 then return false end
        if not NetworkMgr:isOnline() then return false end
        shared.fetching[book_id] = true
        local gen = shared.gen[book_id] or 0
        local base = "/api/koreader/v1/readalong/" .. tostring(info.libraryId) .. "/" .. tostring(book_id)
        local i, failed = 0, 0
        local function step()
            if (shared.gen[book_id] or 0) ~= gen then
                -- the audio was freed meanwhile: stop, and leave nothing behind
                shared.fetching[book_id] = nil
                return
            end
            i = i + 1
            local n = order[i]
            if not n then
                shared.fetching[book_id] = nil
                if failed > 0 then
                    toast(_("Booky: some audio didn't download. Press play there, or use “download the rest” in the read-along menu, to try again."), 5)
                end
                return
            end
            local dest = self:chunkPath(book_id, n)
            self:fetchInChild(base .. "/audio/" .. tostring(n), dest, function(ok)
                if (shared.gen[book_id] or 0) ~= gen then
                    os.remove(dest)
                    shared.fetching[book_id] = nil
                    return
                end
                if not ok then failed = failed + 1 end
                local p = shared.player
                if p and p.book_id == book_id then p:onChunkArrived(n, ok) end
                step()
            end)
        end
        step()
        return true
    end

    -- audioBytes is how much space a book's downloaded audio takes.
    function RA:audioBytes(book_id)
        local dir, total = self:dir(book_id), 0
        if not dir or lfs.attributes(dir, "mode") ~= "directory" then return 0 end
        for name in lfs.dir(dir) do
            if name:match("%.pcm") then
                total = total + (lfs.attributes(dir .. "/" .. name, "size") or 0)
            end
        end
        return total
    end

    -- removeAudio deletes a book's downloaded audio (finished and partial
    -- chunks), keeping the read-along copy and its manifest: pressing play
    -- later downloads what's needed again.
    function RA:removeAudio(book_id)
        local p = shared.player
        if p and p.book_id == book_id then p:stop(true) end
        shared.gen[book_id] = (shared.gen[book_id] or 0) + 1
        local dir = self:dir(book_id)
        if not dir or lfs.attributes(dir, "mode") ~= "directory" then return end
        for name in lfs.dir(dir) do
            if name:match("^%d%d%d%d%.pcm") then os.remove(dir .. "/" .. name) end
        end
    end

    -- forget drops everything the read-along at path left on the device:
    -- its audio folder and its entry. The plain copy of the book is a
    -- different file and is never touched.
    function RA:forget(path)
        local info = self:bookFor(path)
        if not info then return end
        self:removeAudio(info.id)
        local dir = self:dir(info.id)
        if dir then
            os.remove(dir .. "/manifest.json")
            os.remove(dir)
        end
        local known = self:known()
        known[path] = nil
        self.settings:saveSetting("readalong_books", known)
        self.settings:flush()
    end

    -- readPlace notes where the reader is in the book at path, before its
    -- file is swapped for the read-along edition.
    function RA:readPlace(path)
        local ok, DocSettings = pcall(require, "docsettings")
        if not ok or not DocSettings:hasSidecarFile(path) then return nil end
        local ds = DocSettings:open(path)
        return { xp = ds:readSetting("last_xpointer"), percent = ds:readSetting("percent_finished") }
    end

    -- keepPlace puts the reader back where they were in the new file. A
    -- position inside a paragraph doesn't survive the sentence marks, so it
    -- becomes the start of that paragraph (or, failing that, the same
    -- percentage). KOReader's notes for the book stay in its settings but
    -- may no longer point at the right words.
    function RA:keepPlace(path, place)
        local ok, DocSettings = pcall(require, "docsettings")
        if not ok or not place then return end
        local ds = DocSettings:open(path)
        local xp = U.elementXPointer(place.xp)
        if xp then
            ds:saveSetting("last_xpointer", xp)
        elseif place.percent then
            ds:delSetting("last_xpointer")
            ds:saveSetting("last_percent", place.percent)
        end
        ds:flush()
        -- the file browser caches what it knows about each book
        local okb, BookList = pcall(require, "ui/widget/booklist")
        if okb and BookList and BookList.resetBookInfoCache then pcall(BookList.resetBookInfoCache, path) end
    end

    -- sweep forgets read-alongs whose book was deleted (the file browser's
    -- own Delete), so their audio doesn't sit there unseen.
    function RA:sweep()
        for path, _info in pairs(self:known()) do
            if lfs.attributes(path, "mode") ~= "file" then self:forget(path) end
        end
    end

    function RA:audioReady(book_id)
        local m = self:loadManifest(book_id)
        if not m then return 0, 0 end
        local have = 0
        for n = 1, #m.chunks do
            if lfs.attributes(self:chunkPath(book_id, n), "mode") == "file" then have = have + 1 end
        end
        return have, #m.chunks
    end

    -- ---- position ----

    function RA:localPosition(book_id)
        local p = self.settings:readSetting("readalong_positions") or {}
        return p[tostring(book_id)]
    end

    function RA:saveLocalPosition(book_id, pos)
        local p = self.settings:readSetting("readalong_positions") or {}
        p[tostring(book_id)] = { pos = pos, at = os.time() }
        self.settings:saveSetting("readalong_positions", p)
        self.settings:flush()
    end

    -- pushPosition shares the place with the web player, when online.
    function RA:pushPosition(info, pos)
        if not NetworkMgr:isOnline() then return end
        local http = require("socket.http")
        local ltn12 = require("ltn12")
        local socketutil = require("socketutil")
        local body = json.encode({ position = pos })
        socketutil:set_timeout(5, 10)
        pcall(http.request, {
            url = plugin.config.server_url .. "/api/koreader/v1/readalong/"
                .. tostring(info.libraryId) .. "/" .. tostring(info.id) .. "/position",
            method = "PUT",
            headers = {
                ["Authorization"] = "Bearer " .. plugin.config.token,
                ["Content-Type"] = "application/json",
                ["Content-Length"] = tostring(#body),
            },
            source = ltn12.source.string(body),
            sink = ltn12.sink.null(),
        })
        socketutil:reset_timeout()
    end

    -- ---- the player ----

    local Player = {}
    Player.__index = Player

    -- volume reads the Kindle's audio volume (0-100), or nil elsewhere.
    function RA:volume()
        if not Device:isKindle() then return nil end
        local h = io.popen("lipc-get-prop com.lab126.audiomgrd speakerVolume 2>/dev/null")
        if not h then return nil end
        local v = tonumber((h:read("*a") or ""):match("(%d+)"))
        h:close()
        return v
    end

    -- setVolume sets it; the Kindle has no volume buttons.
    function RA:setVolume(v)
        v = math.max(0, math.min(100, math.floor(tonumber(v) or 50)))
        os.execute("lipc-set-prop com.lab126.audiomgrd speakerVolume " .. v .. " >>" .. U.shellQuote(LOG) .. " 2>&1")
        note("volume set to", v, "now reads", self:volume())
    end

    -- soundTest plays two seconds of hiss through the same path the
    -- narration takes, and logs what the audio sink offers — for audio
    -- that plays but isn't heard. done(ok) says whether the player ran
    -- to the end.
    function RA:soundTest(done)
        local backend = self:backend()
        local sink = U.sinkCommand(backend, 22050)
        if not sink then
            note("sound test: no player here (", backend, ")")
            return done(false)
        end
        note("sound test: player", backend)
        if backend == "gst1" or backend == "gst010" then
            local inspect = backend == "gst1" and "gst-inspect-1.0" or "gst-inspect-0.10"
            os.execute(inspect .. " mixersink 2>&1 | grep -iE 'stream-type|enum|\\(|volume|mute|device' | head -40 >>"
                .. U.shellQuote(LOG))
            os.execute("lipc-set-prop com.lab126.audiomgrd setFocus Music >>" .. U.shellQuote(LOG) .. " 2>&1")
        end
        local pid = spawn("head -c 88200 /dev/urandom | " .. sink)
        local waited = 0
        local function check()
            if alive(pid) and waited < 10 then
                waited = waited + 0.5
                UIManager:scheduleIn(0.5, check)
                return
            end
            local ok = not alive(pid)
            if not ok then killGroup(pid, 9) end
            note("sound test:", ok and "the player finished" or "the player hung; killed")
            done(ok)
        end
        UIManager:scheduleIn(0.5, check)
    end

    -- backend picks how to make sound here.
    function RA:backend()
        if self._backend then return self._backend end
        local b = "clock"
        if Device:isKindle() then
            if which("gst-launch-0.10") then b = "gst010" elseif which("gst-launch-1.0") then b = "gst1" end
        elseif which("aplay") then
            b = "aplay"
        elseif which("paplay") then
            b = "paplay"
        end
        self._backend = b
        logger.info("Booky read-along: audio backend", b)
        return b
    end

    -- stretcher finds what plays the narration at another speed: KOReader's
    -- own LuaJIT (KOReader runs from its folder) and the stretch script
    -- beside this file. nil when either is missing.
    function RA:stretcher()
        if self._stretcher ~= nil then return self._stretcher or nil end
        local here = debug.getinfo(1, "S").source:match("^@(.*/)") or ""
        local script = here .. "booky_stretch.lua"
        local luajit
        for _k, cand in ipairs({ ((lfs.currentdir and lfs.currentdir()) or ".") .. "/luajit", "/usr/bin/luajit", "/usr/local/bin/luajit" }) do
            if lfs.attributes(cand, "mode") == "file" then
                luajit = cand
                break
            end
        end
        if luajit and lfs.attributes(script, "mode") == "file" then
            self._stretcher = { luajit = luajit, script = script }
        else
            self._stretcher = false
            logger.info("Booky read-along: no speed control — luajit", luajit, "script", script)
        end
        return self._stretcher or nil
    end

    -- startKeepalive streams silence for a while, so the Bluetooth link
    -- stays awake between plays — a pause, or a move to another book. It's
    -- shared: the next play, in whichever book, ends it.
    function RA:startKeepalive(backend, rate)
        local cmd = U.keepaliveCommand(backend, rate)
        if not cmd or shared.keepalive then return end
        local pid = spawn(cmd)
        shared.keepalive = pid
        note("keeping the Bluetooth link awake, pid", pid)
        UIManager:scheduleIn(600, function()
            if shared.keepalive == pid then
                stopGroup(pid)
                shared.keepalive = nil
            end
        end)
    end

    -- endKeepalive stops the silence after delay seconds (once a new stream
    -- has the link).
    function RA:endKeepalive(delay)
        local pid = shared.keepalive
        if not pid then return end
        shared.keepalive = nil
        if delay and delay > 0 then
            UIManager:scheduleIn(delay, function() stopGroup(pid) end)
        else
            stopGroup(pid)
        end
    end

    -- open starts following the open document, if it is a read-along.
    function RA:attach(ui)
        local doc = ui and ui.document
        local info = doc and self:bookFor(doc.file)
        if not info then return nil end
        local m = self:loadManifest(info.id)
        if not m then
            note("book", info.id, "has no usable manifest on the device")
            return nil
        end
        local have, total = self:audioReady(info.id)
        note("opened book", info.id, "audio parts on device", have, "of", total)
        local p = setmetatable({
            ra = self, ui = ui, info = info, book_id = info.id, m = m,
            rate = m.audio.rate, units = m.units, chunks = m.chunks,
            backend = self:backend(),
            playing = false, pos = 0, current = 0,
            follow = self.settings:readSetting("readalong_follow") ~= false,
            delay = tonumber(self.settings:readSetting("readalong_delay")) or 0.3,
            speed = U.clampSpeed(self.settings:readSetting("readalong_speed") or 1),
        }, Player)
        if not self:stretcher() then p.speed = 1 end
        local saved = self:localPosition(info.id)
        p.pos = saved and saved.pos or 0
        if shared.player and shared.player ~= p then shared.player:stop(true) end
        shared.player = p
        self.player = p
        return p
    end

    function RA:detach()
        if self.player then
            note("closed book", self.player.book_id)
            self.player:stop(true, true)
        end
        if shared.player == self.player then shared.player = nil end
        self.player = nil
    end

    -- xpointers for sentence i: start of its text, end of its text
    function Player:range(i)
        local u = self.units[i]
        if not u then return nil end
        self._ranges = self._ranges or {}
        local r = self._ranges[i]
        if r ~= nil then return r or nil end
        local doc = self.ui.document
        local ok, el = pcall(function() return doc:getNormalizedXPointer("#" .. U.crengineId(u.frag, u.id)) end)
        if not ok or not el or el == "" then
            self._ranges[i] = false
            return nil
        end
        local text = ""
        pcall(function() text = doc:getTextFromXPointer(el) or "" end)
        local n = U.utf8len(text)
        r = { el = el, pos0 = el .. "/text().0", pos1 = el .. "/text()." .. tostring(n), len = n }
        self._ranges[i] = r
        return r
    end

    -- pages i's sentence starts and ends on
    function Player:pagesOf(i)
        local r = self:range(i)
        if not r then return nil end
        local doc = self.ui.document
        local ok0, p0 = pcall(function() return doc:getPageFromXPointer(r.pos0) end)
        local ok1, p1 = pcall(function() return doc:getPageFromXPointer(r.pos1) end)
        if not ok0 or not p0 then return nil end
        return p0, (ok1 and p1) or p0
    end

    -- spill: when the sentence being read runs on past the page(s) on
    -- screen, the time its narration reaches the next page's words, and
    -- that page; nil when it ends on screen. Worked out once per sentence
    -- and page shown.
    function Player:spill(i)
        local ui, doc = self.ui, self.ui.document
        if not ui.rolling or ui.view.view_mode ~= "page" then return nil end
        local cur = doc:getCurrentPage()
        local s = self._spill
        if s and s.i == i and s.cur == cur then return s.at, s.page end
        s = { i = i, cur = cur }
        self._spill = s
        local u, r = self.units[i], self:range(i)
        local _p0, p1 = self:pagesOf(i)
        if not r or not p1 then return nil end
        local visible = doc.getVisiblePageCount and doc:getVisiblePageCount() or 1
        local last = cur + visible - 1
        if p1 <= last then return nil end
        local before
        local okx, xp = pcall(function() return doc:getPageXPointer(last + 1) end)
        if okx and xp then
            local okt, text = pcall(function() return doc:getTextFromXPointers(r.pos0, xp) end)
            if okt and type(text) == "string" then before = U.utf8len(text) end
        end
        s.at, s.page = U.spillAt(u.begin, u["end"], before, r.len), last + 1
        return s.at, s.page
    end

    function Player:pageOf(i)
        local r = self:range(i)
        if not r then return nil end
        local ok, page = pcall(function() return self.ui.document:getPageFromXPointer(r.pos0) end)
        return ok and page or nil
    end

    -- clock: where the narration is now
    function Player:now()
        if not self.playing then return self.pos end
        local elapsed = time.to_number(time.now() - self.started_at)
        return self.started_pos + math.max(0, elapsed - self.delay) * (self.playing_speed or 1)
    end

    -- setSpeed changes how fast the narration plays; playing, it carries
    -- on from where it is at the new speed. Kept for every book.
    function Player:setSpeed(x)
        x = U.clampSpeed(x)
        if x ~= 1 and not self.ra:stretcher() then return false end
        self.speed = x
        self.ra.settings:saveSetting("readalong_speed", x)
        self.ra.settings:flush()
        if self.playing then
            local pos = self:now()
            self:killContent()
            self.playing = false
            self:play(pos)
        else
            self:changed()
        end
        return true
    end

    -- streamEnd is where the downloaded audio runs out, from chunk c on.
    function Player:streamEnd(c)
        local last = c
        while self.chunks[last + 1]
            and lfs.attributes(self.ra:chunkPath(self.book_id, last + 1), "mode") == "file" do
            last = last + 1
        end
        return self.chunks[last].start + self.chunks[last].duration, last
    end

    function Player:haveChunk(c)
        return lfs.attributes(self.ra:chunkPath(self.book_id, c), "mode") == "file"
    end

    -- play starts the narration at pos (seconds into the book).
    function Player:play(pos)
        if pos then self.pos = math.max(0, pos) end
        local c = U.chunkAt(self.chunks, self.pos)
        note("play book", self.book_id, "at", string.format("%.1f", self.pos), "part", c, "of", #self.chunks,
            "on device", self:haveChunk(c), "player", self.backend, "speed", self.speed)
        if not self:haveChunk(c) then
            self.playing = false
            self:release() -- no need to keep the device awake while waiting
            if self.ra:fetchAudio(self.book_id, c) then
                note("waiting for part", c, "to download")
                self.waiting = c
                toast(_("Booky: downloading the audio for this part — playback starts when it arrives"), 4)
            else
                note("part", c, "isn't on the device and can't be downloaded now")
                self.waiting = nil
                toast(_("Booky: the audio for this part isn't on the device yet. Connect to wifi, then press play again."), 5)
            end
            self:changed()
            return
        end
        self.waiting = nil
        self:killContent()
        local stream_end, last = self:streamEnd(c)
        self.stream_end = stream_end
        if self.backend ~= "clock" then
            if self.backend == "gst010" or self.backend == "gst1" then
                os.execute("lipc-set-prop com.lab126.audiomgrd setFocus Music >>" .. U.shellQuote(LOG) .. " 2>&1")
                -- what the Kindle's audio manager says it's doing (focus,
                -- volume, outputs), for audio that plays but isn't heard
                os.execute("command -v lipc-probe >/dev/null 2>&1 && lipc-probe -v com.lab126.audiomgrd 2>&1 | head -40 >>"
                    .. U.shellQuote(LOG))
            end
            local segments = {}
            for k = c, last do
                table.insert(segments, {
                    path = self.ra:chunkPath(self.book_id, k),
                    skip = k == c and U.pcmOffset(self.pos, self.chunks[c].start, self.rate) or 0,
                })
            end
            local stretch
            local st = self.ra:stretcher()
            if st then stretch = U.stretchCommand(st.luajit, st.script, self.speed, self.rate) end
            local cmd = U.feedCommand(segments) .. (stretch and (" | " .. stretch) or "")
                .. " | " .. U.sinkCommand(self.backend, self.rate)
            self.pid = spawn(cmd)
            note("started pid", self.pid, "parts", c, "to", last, "until", string.format("%.0f", stream_end))
            self.spawned_at = time.now()
        end
        self.playing_speed = self.speed
        self.playing = true
        self.started_at = time.now()
        self.started_pos = self.pos
        -- the silence can stop once the content stream is up
        self.ra:endKeepalive(1.5)
        if not self.standby_prevented then
            UIManager:preventStandby()
            self.standby_prevented = true
        end
        self:holdAwake(true)
        self.current = 0
        self:changed()
        self:tick()
    end

    -- changed tells the reader the state moved (the status bar shows it)
    function Player:changed()
        if self.onChange then pcall(self.onChange, self) end
    end

    function Player:killContent()
        if self.pid then
            stopGroup(self.pid)
            self.pid = nil
        end
        -- belt and braces, for a shell that didn't make a process group
        os.execute(U.strayKillCommand())
    end

    function Player:pause()
        if not self.playing then return end
        note("pause book", self.book_id, "at", string.format("%.1f", self:now()))
        self.pos = self:now()
        self.playing = false
        UIManager:unschedule(self._tick)
        self:killContent()
        self.ra:startKeepalive(self.backend, self.rate)
        self:release()
        self:savePosition()
        self:changed()
    end

    -- stop ends playback. keepLink: the reader is moving to another book —
    -- the Bluetooth link is kept awake as on a pause, for the next one.
    function Player:stop(quiet, keepLink)
        local was = self.playing
        if self.playing then self.pos = self:now() end
        self.playing = false
        UIManager:unschedule(self._tick)
        self:killContent()
        if keepLink then
            if was then self.ra:startKeepalive(self.backend, self.rate) end
        else
            self.ra:endKeepalive(0)
        end
        self:release()
        self:unhighlight()
        if was or not quiet then self:savePosition() end
        self:changed()
    end

    function Player:release()
        if self.standby_prevented then
            UIManager:allowStandby()
            self.standby_prevented = false
        end
        self:holdAwake(false)
    end

    -- holdAwake keeps the device from sleeping mid-chapter: KOReader's own
    -- idle timer, and on a Kindle the system's screensaver timer.
    function Player:holdAwake(on)
        local ok, PluginShare = pcall(require, "pluginshare")
        if ok and PluginShare then PluginShare.pause_auto_suspend = on or nil end
        if Device:isKindle() then
            os.execute("lipc-set-prop com.lab126.powerd preventScreenSaver " .. (on and "1" or "0") .. " >/dev/null 2>&1")
        end
    end

    function Player:savePosition()
        self.ra:saveLocalPosition(self.book_id, self.pos)
        self.ra:pushPosition(self.info, self.pos)
    end

    function Player:toggle()
        if self.playing then self:pause() else self:playHere() end
    end

    -- playHere carries on from where the narration was if that sentence is
    -- on screen; otherwise starts at the first sentence on this page.
    function Player:playHere()
        local doc = self.ui.document
        local page = doc:getCurrentPage()
        local i = U.unitAt(self.units, self.pos)
        -- the narration's sentence, if it's on this page (or runs onto it)
        local p0, p1
        if i > 0 then p0, p1 = self:pagesOf(i) end
        if p0 and p0 <= page and page <= p1 then return self:play() end
        local first = U.firstUnitFrom(#self.units, function(k) return self:pageOf(k) end, page)
        if first then return self:play(self.units[first].begin) end
        return self:play()
    end

    function Player:skip(n)
        local i = U.unitAt(self.units, self:now())
        local target = math.max(1, math.min(#self.units, i + n))
        if n < 0 and i > 0 and self:now() - self.units[i].begin > 1.5 then target = i end
        local was = self.playing
        if was then self:killContent() end
        self.playing = false
        self.pos = self.units[target].begin
        if was then self:play() else self:show(target, true) end
    end

    -- onChunkArrived: a download finished (ok) or gave up. Playback that
    -- was waiting for it starts, or stops waiting and says why.
    function Player:onChunkArrived(n, ok)
        if self.waiting ~= n then return end
        note("part", n, ok and "downloaded" or "failed to download")
        if ok then return self:play() end
        self.waiting = nil
        toast(_("Booky: couldn't download the audio for this part. Check the connection, then press play again."), 5)
        self:changed()
    end

    -- tick moves the highlight along and sleeps until the next sentence.
    function Player:tick()
        UIManager:unschedule(self._tick)
        if not self.playing then return end
        local pos = self:now()
        -- the content stream died before its audio ran out: it never started
        -- (no headphones, no player), or the sound path dropped it
        if self.pid and not alive(self.pid) and pos < self.stream_end - 2 then
            local young = time.to_number(time.now() - self.spawned_at) < 3
            note("the player stopped by itself after", string.format("%.1f", time.to_number(time.now() - self.spawned_at)), "s",
                "at", string.format("%.1f", pos), "of", string.format("%.0f", self.stream_end))
            self:pause()
            if young then
                toast(_("Booky: couldn't play audio. Connect Bluetooth headphones in the Kindle's settings, then try again.\nDetails: menu → Read-along → Playback log."), 8)
            else
                toast(_("Booky: the audio stopped — paused. Check the headphones, then play again."), 6)
            end
            return
        end
        if pos >= self.stream_end then
            local c = U.chunkAt(self.chunks, self.stream_end + 0.01)
            if self.chunks[c].start < self.stream_end then
                -- the end of the book
                self:stop()
                toast(_("Booky: the end of the audiobook"))
                return
            end
            self.pos = self.stream_end
            self.playing = false
            self:play() -- picks up the next chunk, or waits for it
            return
        end
        -- keep the place on the device now and then, in case it dies mid-chapter
        if not self.saved_at or time.to_number(time.now() - self.saved_at) > 60 then
            self.saved_at = time.now()
            self.ra:saveLocalPosition(self.book_id, pos)
        end
        local i = U.unitAt(self.units, pos)
        if i ~= self.current then
            self.current = i
            if i > 0 then self:show(i) end
        end
        local wake = U.nextWake(self.units, i, pos, self.stream_end, self.playing_speed)
        -- a sentence running onto the next page: turn when the narration
        -- gets there, not when the next sentence starts
        if i > 0 and self.follow then
            local at, page = self:spill(i)
            if at and pos >= at then
                self.ui:handleEvent(Event:new("GotoPage", page))
                self:show(i)
            elseif at then
                wake = math.max(0.05, math.min(wake, (at - pos) / (self.playing_speed or 1)))
            end
        end
        self._tick = self._tick or function() self:tick() end
        UIManager:scheduleIn(wake, self._tick)
    end

    -- show highlights sentence i, turning the page to it first if needed.
    function Player:show(i, force)
        local r = self:range(i)
        if not r then return end
        local ui, doc = self.ui, self.ui.document
        local function boxesOf()
            local ok, boxes = pcall(function() return doc:getScreenBoxesFromPositions(r.pos0, r.pos1, true) end)
            if ok and boxes and #boxes > 0 then return boxes end
            -- the measured range didn't resolve: let the engine find the
            -- sentence around its start instead
            local a, z
            pcall(function() a, z = doc:extendXPointersToSentenceSegment(r.pos0, r.pos0) end)
            if a and z then
                ok, boxes = pcall(function() return doc:getScreenBoxesFromPositions(a, z, true) end)
                if ok and boxes and #boxes > 0 then return boxes end
            end
            return nil
        end
        if (self.follow or force) and ui.rolling then
            if ui.view.view_mode == "page" then
                -- a sentence across two pages is on screen if either part is
                local p0, p1 = self:pagesOf(i)
                local cur = doc:getCurrentPage()
                local visible = doc.getVisiblePageCount and doc:getVisiblePageCount() or 1
                if p0 and (p1 < cur or p0 > cur + visible - 1) then
                    ui:handleEvent(Event:new("GotoPage", p0))
                end
            elseif not boxesOf() then
                -- scroll mode: move only when the sentence is off screen
                ui:handleEvent(Event:new("GotoXPointer", r.pos0))
            end
        end
        -- after any page turn: a page update clears temporary highlights
        local boxes = boxesOf()
        local ok = boxes ~= nil
        local old = self.boxes
        self.boxes = ok and boxes and #boxes > 0 and boxes or nil
        ui.view.highlight.temp = {}
        if self.boxes then ui.view.highlight.temp[doc:getCurrentPage()] = self.boxes end
        local region
        for _k, list in ipairs({ old or {}, self.boxes or {} }) do
            for _j, b in ipairs(list) do
                region = region and region:combine(b) or Geom:new{ x = b.x, y = b.y, w = b.w, h = b.h }
            end
        end
        if region then UIManager:setDirty(ui.dialog, "ui", region) end
    end

    function Player:unhighlight()
        if not self.boxes then return end
        local region
        for _j, b in ipairs(self.boxes) do
            region = region and region:combine(b) or Geom:new{ x = b.x, y = b.y, w = b.w, h = b.h }
        end
        self.boxes = nil
        self.ui.view.highlight.temp = {}
        if region then UIManager:setDirty(self.ui.dialog, "ui", region) end
    end

    return RA
end

return M
