#!/usr/bin/python3
"""Tiny X11 input helper for verifying the indicator by hand-free clicking.

Stands in for xdotool (not installed here) using libXtst through ctypes.

    xinput_fake.py click X Y     left-click at screen position, then put the
                                 pointer back where it was
    xinput_fake.py key Escape    press and release a key (X keysym name);
                                 combos work too, e.g. "alt+F2"
    xinput_fake.py where         print the current pointer position
    xinput_fake.py idle          print milliseconds since the last input

X11 only; does nothing useful under Wayland.
"""
import ctypes
import ctypes.util
import sys
import time

x11 = ctypes.CDLL(ctypes.util.find_library("X11"))
xtst = ctypes.CDLL(ctypes.util.find_library("Xtst"))

x11.XOpenDisplay.restype = ctypes.c_void_p
x11.XOpenDisplay.argtypes = [ctypes.c_char_p]
x11.XDefaultRootWindow.restype = ctypes.c_ulong
x11.XDefaultRootWindow.argtypes = [ctypes.c_void_p]
x11.XStringToKeysym.restype = ctypes.c_ulong
x11.XStringToKeysym.argtypes = [ctypes.c_char_p]
x11.XKeysymToKeycode.restype = ctypes.c_ubyte
x11.XKeysymToKeycode.argtypes = [ctypes.c_void_p, ctypes.c_ulong]
x11.XFlush.argtypes = [ctypes.c_void_p]
x11.XCloseDisplay.argtypes = [ctypes.c_void_p]
x11.XQueryPointer.argtypes = [
    ctypes.c_void_p, ctypes.c_ulong,
    ctypes.POINTER(ctypes.c_ulong), ctypes.POINTER(ctypes.c_ulong),
    ctypes.POINTER(ctypes.c_int), ctypes.POINTER(ctypes.c_int),
    ctypes.POINTER(ctypes.c_int), ctypes.POINTER(ctypes.c_int),
    ctypes.POINTER(ctypes.c_uint)]
xtst.XTestFakeMotionEvent.argtypes = [
    ctypes.c_void_p, ctypes.c_int, ctypes.c_int, ctypes.c_int, ctypes.c_ulong]
xtst.XTestFakeButtonEvent.argtypes = [
    ctypes.c_void_p, ctypes.c_uint, ctypes.c_int, ctypes.c_ulong]
xtst.XTestFakeKeyEvent.argtypes = [
    ctypes.c_void_p, ctypes.c_uint, ctypes.c_int, ctypes.c_ulong]


def pointer(display):
    root = x11.XDefaultRootWindow(display)
    r, c = ctypes.c_ulong(), ctypes.c_ulong()
    rx, ry, wx, wy = (ctypes.c_int() for _ in range(4))
    mask = ctypes.c_uint()
    x11.XQueryPointer(display, root, ctypes.byref(r), ctypes.byref(c),
                      ctypes.byref(rx), ctypes.byref(ry),
                      ctypes.byref(wx), ctypes.byref(wy), ctypes.byref(mask))
    return rx.value, ry.value


MODIFIERS = {"alt": "Alt_L", "ctrl": "Control_L", "shift": "Shift_L", "super": "Super_L"}


class XScreenSaverInfo(ctypes.Structure):
    _fields_ = [("window", ctypes.c_ulong), ("state", ctypes.c_int),
                ("kind", ctypes.c_int), ("til_or_since", ctypes.c_ulong),
                ("idle", ctypes.c_ulong), ("event_mask", ctypes.c_ulong)]


def idle_ms(display):
    """Milliseconds since the last keyboard or mouse input (real or faked)."""
    xss = ctypes.CDLL(ctypes.util.find_library("Xss"))
    xss.XScreenSaverAllocInfo.restype = ctypes.POINTER(XScreenSaverInfo)
    xss.XScreenSaverQueryInfo.argtypes = [
        ctypes.c_void_p, ctypes.c_ulong, ctypes.POINTER(XScreenSaverInfo)]
    info = xss.XScreenSaverAllocInfo()
    xss.XScreenSaverQueryInfo(display, x11.XDefaultRootWindow(display), info)
    return info.contents.idle


def move(display, x, y):
    xtst.XTestFakeMotionEvent(display, -1, x, y, 0)
    x11.XFlush(display)


def main() -> int:
    if len(sys.argv) < 2:
        print(__doc__, file=sys.stderr)
        return 2
    display = x11.XOpenDisplay(None)
    if not display:
        print("cannot open X display", file=sys.stderr)
        return 1
    try:
        command = sys.argv[1]
        if command == "where":
            print(*pointer(display))
        elif command == "click":
            x, y = int(sys.argv[2]), int(sys.argv[3])
            back = pointer(display)
            move(display, x, y)
            time.sleep(0.15)
            xtst.XTestFakeButtonEvent(display, 1, 1, 0)
            x11.XFlush(display)
            time.sleep(0.05)
            xtst.XTestFakeButtonEvent(display, 1, 0, 0)
            x11.XFlush(display)
            time.sleep(0.15)
            move(display, *back)
        elif command == "key":
            # "Escape", or a combo such as "alt+F2": hold the modifiers,
            # tap the last key, release in reverse order.
            names = [MODIFIERS.get(part.lower(), part) for part in sys.argv[2].split("+")]
            keycodes = [x11.XKeysymToKeycode(display, x11.XStringToKeysym(name.encode()))
                        for name in names]
            if not all(keycodes):
                print(f"unknown key in {sys.argv[2]!r}", file=sys.stderr)
                return 1
            for keycode in keycodes:
                xtst.XTestFakeKeyEvent(display, keycode, 1, 0)
                x11.XFlush(display)
                time.sleep(0.03)
            for keycode in reversed(keycodes):
                xtst.XTestFakeKeyEvent(display, keycode, 0, 0)
                x11.XFlush(display)
                time.sleep(0.03)
        elif command == "idle":
            print(idle_ms(display))
        else:
            print(__doc__, file=sys.stderr)
            return 2
    finally:
        x11.XCloseDisplay(display)
    return 0


if __name__ == "__main__":
    sys.exit(main())
