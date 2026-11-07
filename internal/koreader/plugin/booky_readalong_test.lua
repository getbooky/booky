-- Tests for the read-along helpers that don't need KOReader.
-- Run: luajit booky_readalong_test.lua  (from this directory)
package.path = "./?.lua;" .. package.path
local U = require("booky_readalong").util

local failures = 0
local function check(name, got, want)
    if got ~= want then
        failures = failures + 1
        io.stderr:write(("FAIL %s: got %s, want %s\n"):format(name, tostring(got), tostring(want)))
    end
end

-- unitAt: the last sentence that has begun
local units = { { begin = 1, ["end"] = 2 }, { begin = 2.5, ["end"] = 4 }, { begin = 4, ["end"] = 6 } }
check("unitAt before", U.unitAt(units, 0.5), 0)
check("unitAt first", U.unitAt(units, 1), 1)
check("unitAt gap", U.unitAt(units, 2.2), 1)
check("unitAt last", U.unitAt(units, 99), 3)
check("unitAt edge", U.unitAt(units, 3.96), 3) -- within the 50 ms lead
check("unitAt empty", U.unitAt({}, 5), 0)

-- chunkAt
local chunks = { { start = 0, duration = 900 }, { start = 900, duration = 900 }, { start = 1800, duration = 30 } }
check("chunkAt 0", U.chunkAt(chunks, 0), 1)
check("chunkAt mid", U.chunkAt(chunks, 1000), 2)
check("chunkAt boundary", U.chunkAt(chunks, 900), 2)
check("chunkAt end", U.chunkAt(chunks, 5000), 3)
check("chunkAt negative", U.chunkAt(chunks, -1), 1)

-- the engine's id for a sentence
check("crengineId", U.crengineId(3, "booky-s12"), "_doc_fragment_3_ booky-s12")

-- characters, not bytes
check("utf8len ascii", U.utf8len("Alice"), 5)
check("utf8len accents", U.utf8len("héllo—“x”"), 9)
check("utf8len empty", U.utf8len(""), 0)

-- byte offsets land on whole samples
check("pcmOffset", U.pcmOffset(1901.5, 1800, 22050), 101.5 * 22050 * 2)
check("pcmOffset even", U.pcmOffset(0.0000453, 0, 22050) % 2, 0)
check("pcmOffset before start", U.pcmOffset(10, 20, 22050), 0)

-- firstUnitFrom: binary search over pages
local pages = { 1, 1, 2, 2, 2, 5, 6 }
local calls = 0
local function pageOf(i) calls = calls + 1; return pages[i] end
check("firstUnitFrom 2", U.firstUnitFrom(#pages, pageOf, 2), 3)
check("firstUnitFrom 3 (no sentence starts there)", U.firstUnitFrom(#pages, pageOf, 3), 6)
check("firstUnitFrom 1", U.firstUnitFrom(#pages, pageOf, 1), 1)
check("firstUnitFrom past the end", U.firstUnitFrom(#pages, pageOf, 9), nil)
check("firstUnitFrom unresolved pages count as before",
    U.firstUnitFrom(3, function(i) return ({ nil, 4, 4 })[i] end, 4), 2)
calls = 0
U.firstUnitFrom(1000, function(i) calls = calls + 1; return i end, 700)
check("firstUnitFrom is a binary search", calls <= 11, true)

-- nextWake: sleep to the next sentence, never too long or too short
check("nextWake next sentence", U.nextWake(units, 1, 1.5, 100), 1)
check("nextWake stream end first", U.nextWake(units, 1, 1.5, 2), 0.5)
check("nextWake clamp high", U.nextWake({ { begin = 0 } }, 1, 0, 1000), 5)
check("nextWake clamp low", U.nextWake(units, 1, 2.49, 100), 0.05)

-- player commands
local gst = U.sinkCommand("gst010", 22050)
check("gst010 sink", gst:find("mixersink stream-type=Music sync=true", 1, true) ~= nil, true)
check("gst010 caps", gst:find("rate=22050,channels=1", 1, true) ~= nil, true)
check("gst1 sink", U.sinkCommand("gst1", 22050):find("do-timestamp=true", 1, true) ~= nil, true)
check("clock has no sink", U.sinkCommand("clock", 22050), nil)
check("keepalive only on kindle", U.keepaliveCommand("aplay", 22050), nil)
check("keepalive reads silence", U.keepaliveCommand("gst1", 22050):find("/dev/zero", 1, true) ~= nil, true)

-- feedCommand, run for real through sh: offsets, joins, awkward names
local dir = os.tmpname()
os.remove(dir)
os.execute("mkdir -p " .. U.shellQuote(dir))
local function write(name, data)
    local f = assert(io.open(dir .. "/" .. name, "wb"))
    f:write(data)
    f:close()
    return dir .. "/" .. name
end
local a = write("it's a $(chunk) 1.pcm", "0123456789")
local b = write("chunk `2`.pcm", "abcde")
local function run(cmd)
    local h = assert(io.popen(cmd))
    local out = h:read("*a")
    h:close()
    return out
end
check("feed from offset, then next chunk", run(U.feedCommand({ { path = a, skip = 4 }, { path = b, skip = 0 } })), "456789abcde")
check("feed from start", run(U.feedCommand({ { path = a, skip = 0 } })), "0123456789")
check("feed one chunk late", run(U.feedCommand({ { path = b, skip = 3 } })), "de")
check("shellQuote round trip", run("printf %s " .. U.shellQuote("it's $HOME `x` \"q\"")), "it's $HOME `x` \"q\"")
os.execute("rm -rf " .. U.shellQuote(dir))

-- validManifest: only what the server promises gets trusted
local function manifest()
    return {
        audio = { format = "s16le", rate = 22050, channels = 1 },
        chunks = { { n = 1, start = 0, duration = 10 } },
        units = { { id = "booky-s2", frag = 1, begin = 5, ["end"] = 6 }, { id = "booky-s1", frag = 1, begin = 1, ["end"] = 2 } },
    }
end
local m = manifest()
check("valid manifest", U.validManifest(m), true)
check("units sorted by time", m.units[1].id, "booky-s1")
m = manifest(); m.audio.format = "mp3"
check("other audio formats", U.validManifest(m), false)
m = manifest(); m.units[1].id = "x'); os.execute('rm -rf /"
check("ids are ours only", U.validManifest(m), false)
m = manifest(); m.chunks[1].n = 2
check("chunks numbered from 1", U.validManifest(m), false)
m = manifest(); m.units = {}
check("no units", U.validManifest(m), false)
check("not a table", U.validManifest("nope"), false)

if failures > 0 then
    io.stderr:write(failures .. " failure(s)\n")
    os.exit(1)
end
print("ok")
