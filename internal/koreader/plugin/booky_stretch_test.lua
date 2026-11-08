-- Tests for the narration speed changer. Needs LuaJIT (ffi).
-- Run: luajit booky_stretch_test.lua  (from this directory)
package.path = "./?.lua;" .. package.path
local S = require("booky_stretch")
local ffi = require("ffi")

local failures = 0
local function check(name, ok, detail)
    if not ok then
        failures = failures + 1
        io.stderr:write(("FAIL %s %s\n"):format(name, detail or ""))
    end
end

local rate = 22050

local function tone(secs, hz)
    local n = math.floor(secs * rate)
    local a = ffi.new("int16_t[?]", n)
    for i = 0, n - 1 do a[i] = math.floor(8000 * math.sin(2 * math.pi * hz * i / rate)) end
    return ffi.string(a, n * 2)
end

-- stretch runs input through at speed, fed in pieces of `piece` bytes
local function stretch(input, speed, piece)
    local out, pos = {}, 1
    S.run(speed, rate, function()
        if pos > #input then return nil end
        local c = input:sub(pos, pos + piece - 1)
        pos = pos + piece
        return c
    end, function(b) out[#out + 1] = b end)
    return table.concat(out)
end

-- pitch from zero crossings, in Hz
local function pitch(bytes)
    local n = #bytes / 2
    local a = ffi.new("int16_t[?]", n)
    ffi.copy(a, bytes, #bytes)
    local c = 0
    for i = 1, n - 1 do
        if (a[i - 1] < 0) ~= (a[i] < 0) then c = c + 1 end
    end
    return c / (n / rate) / 2
end

local input = tone(3, 440)
for _, speed in ipairs({ 0.5, 0.7, 1.1, 1.5, 2.0 }) do
    local out = stretch(input, speed, 16384)
    local secs, want = #out / 2 / rate, 3 / speed
    check(("speed %.1f length"):format(speed), math.abs(secs - want) < 0.05,
        ("got %.3fs, want %.3fs"):format(secs, want))
    local hz = pitch(out)
    check(("speed %.1f keeps the pitch"):format(speed), math.abs(hz - 440) < 10, ("got %.1f Hz"):format(hz))
    check(("speed %.1f whole samples"):format(speed), #out % 2 == 0)
end

-- normal speed passes the bytes through untouched
check("1.0 is untouched", stretch(input, 1, 16384) == input)

-- pieces of any size, odd ones included, come out the same
check("odd pieces", stretch(input, 1.3, 777) == stretch(input, 1.3, 16384))

-- nothing in, nothing much out
check("empty", #stretch("", 1.5, 16384) <= 2 * rate * 0.03)

-- loud input never wraps round
local loud = ffi.new("int16_t[?]", rate)
for i = 0, rate - 1 do loud[i] = (i % 2 == 0) and 32767 or -32768 end
local out = stretch(ffi.string(loud, rate * 2), 1.5, 4096)
local a = ffi.new("int16_t[?]", #out / 2)
ffi.copy(a, out, #out)
local wrapped = false
for i = 1, #out / 2 - 1 do
    if math.abs(a[i] - a[i - 1]) > 65535 then wrapped = true end
end
check("no wrap-around", not wrapped)

-- as a program: stdin to stdout, and bad arguments refused
local dir = os.tmpname()
os.remove(dir)
os.execute("mkdir -p '" .. dir .. "'")
local f = assert(io.open(dir .. "/in.pcm", "wb"))
f:write(input)
f:close()
local ok = os.execute("luajit booky_stretch.lua 1.5 22050 < '" .. dir .. "/in.pcm' > '" .. dir .. "/out.pcm'")
check("program runs", ok == true or ok == 0)
f = io.open(dir .. "/out.pcm", "rb")
local prog = f and f:read("*a") or ""
if f then f:close() end
check("program output", prog == stretch(input, 1.5, 16384), ("%d bytes"):format(#prog))
local bad = os.execute("luajit booky_stretch.lua 3 22050 < /dev/null > /dev/null 2>&1")
check("speed out of range refused", not (bad == true or bad == 0))
os.execute("rm -rf '" .. dir .. "'")

if failures > 0 then
    io.stderr:write(failures .. " failure(s)\n")
    os.exit(1)
end
print("ok")
