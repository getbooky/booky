-- Tests for the read-along's device side, with KOReader stubbed out: audio
-- downloads shared between the file browser's and the reader's copies of
-- the plugin, freeing audio mid-download, and playback waiting for a part
-- that fails to arrive.
-- Run: luajit booky_readalong_device_test.lua  (from this directory)
package.path = "./?.lua;" .. package.path

local failures = 0
local function check(name, got, want)
    if got ~= want then
        failures = failures + 1
        io.stderr:write(("FAIL %s: got %s, want %s\n"):format(name, tostring(got), tostring(want)))
    end
end

local function shq(s) return "'" .. tostring(s):gsub("'", "'\\''") .. "'" end
local function sh(cmd)
    local r = os.execute(cmd)
    return r == true or r == 0
end

-- ---- KOReader, stubbed ----

local toasts, tasks, children = {}, {}, 0
local failing = {} -- chunk numbers the server won't serve
local manifests = {}

local function stub(name, value) package.preload[name] = function() return value end end
stub("device", { isKindle = function() return false end })
stub("ui/event", { new = function(_, ...) return { ... } end })
stub("ui/geometry", { new = function(_, t) return t end })
stub("ui/widget/infomessage", { new = function(_, t) return t end })
stub("ui/network/manager", { isOnline = function() return true end })
stub("ui/uimanager", {
    show = function(_, w) table.insert(toasts, w.text) end,
    scheduleIn = function(_, _secs, fn) table.insert(tasks, fn) end,
    unschedule = function(_, fn)
        for i = #tasks, 1, -1 do if tasks[i] == fn then table.remove(tasks, i) end end
    end,
    preventStandby = function() end,
    allowStandby = function() end,
    setDirty = function() end,
})
stub("ffi", { cdef = function() end, C = { kill = function() return -1 end } })
stub("ffi/util", {
    -- the child runs here and now; its poll sees it done
    runInSubProcess = function(fn) children = children + 1; fn(); return children end,
    isSubProcessDone = function() return true end,
})
stub("json", {
    decode = function(s) return manifests[s] and manifests[s]() or error("bad json") end,
    encode = function() return "{}" end,
})
stub("libs/libkoreader-lfs", {
    attributes = function(path, what)
        if sh("test -d " .. shq(path)) then return what == "mode" and "directory" or 0 end
        local f = io.open(path, "rb")
        if not f then return nil end
        local size = f:seek("end")
        f:close()
        if what == "size" then return size end
        return "file"
    end,
    mkdir = function(path) sh("mkdir -p " .. shq(path)) end,
    dir = function(path)
        local h = io.popen("ls -a " .. shq(path))
        return function()
            local line = h:read("*l")
            if not line then h:close() end
            return line
        end
    end,
})
stub("logger", { info = function() end })
stub("ui/time", { now = function() return os.time() end, to_number = function(t) return t end })
stub("gettext", function(s) return s end)
stub("socketutil", { set_timeout = function() end, reset_timeout = function() end })
stub("ltn12", { sink = {
    file = function(f) return function(chunk) if chunk then f:write(chunk) else f:close() end return 1 end end,
    null = function() return function() return 1 end end,
}, source = { string = function() return function() return nil end end } })
stub("socket.http", { request = function(req)
    local n = tonumber(req.url:match("/audio/(%d+)$"))
    if n and failing[n] then req.sink(nil); return 1, 503 end
    req.sink("pcm data")
    req.sink(nil)
    return 1, 200
end })

local function drain()
    local guard = 0
    while #tasks > 0 do
        guard = guard + 1
        if guard > 1000 then error("scheduled tasks never settle") end
        table.remove(tasks, 1)()
    end
end

-- ---- a book with three parts of audio ----

local root = os.tmpname()
os.remove(root)
sh("mkdir -p " .. shq(root))
local book = root .. "/Alice.epub"
local settings = {
    data = { readalong_books = { [book] = { id = 7, libraryId = 1, title = "Alice" } } },
    readSetting = function(self, k) return self.data[k] end,
    saveSetting = function(self, k, v) self.data[k] = v end,
    flush = function() end,
}
local function plugin()
    return {
        settings = settings,
        config = { server_url = "http://booky", token = "t" },
        downloadRoot = function() return root end,
    }
end
manifests.three = function()
    return {
        audio = { format = "s16le", rate = 22050, channels = 1 },
        chunks = { { n = 1, start = 0, duration = 900 }, { n = 2, start = 900, duration = 900 }, { n = 3, start = 1800, duration = 900 } },
        units = { { id = "booky-s1", frag = 1, begin = 1, ["end"] = 2 } },
    }
end
local ReadAlong = require("booky_readalong")
local browser, reader = ReadAlong.new(plugin()), ReadAlong.new(plugin())
local dir = browser:dir(7)
sh("mkdir -p " .. shq(dir))
local f = assert(io.open(dir .. "/manifest.json", "wb"))
f:write("three")
f:close()
local function chunks()
    local have = 0
    for n = 1, 3 do
        if io.open(browser:chunkPath(7, n), "rb") then have = have + 1 end
    end
    return have
end

-- one download for both copies of the plugin
check("browser starts the download", browser:fetchAudio(7), true)
check("reader joins it", reader:fetchAudio(7), true)
drain()
check("each part fetched once", children, 3)
check("all parts arrived", chunks(), 3)
check("a finished download can start again", reader:fetchAudio(7), false) -- nothing missing

-- freeing the audio stops a download that's running and leaves nothing
browser:removeAudio(7)
check("freed", chunks(), 0)
children = 0
check("download again", browser:fetchAudio(7), true)
reader:removeAudio(7) -- freed from the other copy, mid-download
drain()
check("freed mid-download stays freed", chunks(), 0)
check("the download stopped", children, 1)
check("a new download can start", browser:fetchAudio(7), true)
drain()
check("and finishes", chunks(), 3)

-- playback waiting for a part that won't come stops waiting
browser:removeAudio(7)
failing[1] = true
reader._backend = "clock"
local p = reader:attach({ document = { file = book } })
check("attached", p ~= nil, true)
toasts = {}
p:play(10)
check("waits for its part", p.waiting, 1)
drain()
check("stops waiting", p.waiting, nil)
check("not playing", p.playing, false)
local said = false
for _, t in ipairs(toasts) do
    if t:find("couldn't download the audio for this part", 1, true) then said = true end
end
check("says why", said, true)

-- and a part that does come starts playback
failing[1] = nil
p:play(10)
check("waits again", p.waiting, 1)
local function drain_until_playing()
    local guard = 0
    while not p.playing and #tasks > 0 do
        guard = guard + 1
        if guard > 100 then break end
        table.remove(tasks, 1)()
    end
end
drain_until_playing()
check("plays once it arrives", p.playing, true)
reader:detach()
tasks = {}

sh("rm -rf " .. shq(root))
if failures > 0 then
    io.stderr:write(failures .. " failure(s)\n")
    os.exit(1)
end
print("ok")
