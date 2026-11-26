//go:build !ios

// Not upstream code: this file was added to the copy of fyne.io/systray
// v1.12.2 in agent/third_party/systray of the AI Usage client (see
// PATCHED.md). It is under the AI Usage client's MIT licence (LICENSE at
// the repository's root), not the Apache License 2.0 of the copied code.
//
// Menu items whose view the AI Usage usage panel draws (drawn-panel design
// 6.3). What a view shows and where is decided on the Go side
// (panelview_darwin.go and the PanelViewer); this file measures text in
// the menu's fonts, resolves the system's colours for the view's
// appearance and carries the Go side's drawing steps out. A view item
// sends no action by itself: a click is taken in mouseUp:, Return on a
// highlighted view item in keyDown: (the menu of an app that is not
// active hands the highlighted view the keys it does not use itself, and
// does nothing with Return: measured on CI, stage 3), and Return that
// closes the menu without the item's action (an active app's menu, stage
// 0) when the menu closes.

#import <Cocoa/Cocoa.h>
#include "systray.h"
#include "panelview.h"

// the root menu's state while it is open (main thread only)
static BOOL aiuMenuOpen = NO;
static BOOL aiuActed = NO;                 // an item's action came while it was open
static NSMenuItem *aiuLastHighlighted = nil; // the last item highlighted (nil ones are skipped)

static NSFont *aiu_font(int font) {
  NSFont *menu = [NSFont menuFontOfSize:0];
  switch (font) {
  case 1:
    return [NSFont systemFontOfSize:menu.pointSize weight:NSFontWeightBold];
  case 2:
    return [NSFont systemFontOfSize:menu.pointSize weight:NSFontWeightSemibold];
  }
  return menu;
}

static NSString *aiu_string(const char *s) {
  NSString *out = s ? [NSString stringWithUTF8String:s] : nil;
  return out ? out : @"";
}

// one line of text in font: its height in points
static CGFloat aiu_line(NSFont *f) {
  return [@"Xg" sizeWithAttributes:@{NSFontAttributeName : f}].height;
}

int aiu_text_width(int font, const char *s, int scale) {
  @autoreleasepool {
    NSSize sz = [aiu_string(s) sizeWithAttributes:@{NSFontAttributeName : aiu_font(font)}];
    return (int)ceil(sz.width * (scale < 1 ? 1 : scale));
  }
}

int aiu_line_height(int font, int scale) {
  @autoreleasepool {
    return (int)ceil(aiu_line(aiu_font(font)) * (scale < 1 ? 1 : scale));
  }
}

static NSRect aiu_rect(int x, int y, int w, int h, int scale) {
  CGFloat s = scale < 1 ? 1 : scale;
  return NSMakeRect(x / s, y / s, w / s, h / s);
}

static NSColor *aiu_colour(uint8_t r, uint8_t g, uint8_t b, uint8_t a) {
  return [NSColor colorWithSRGBRed:r / 255.0 green:g / 255.0 blue:b / 255.0 alpha:a / 255.0];
}

void aiu_fill(int x, int y, int w, int h, int radius, uint8_t r, uint8_t g, uint8_t b, uint8_t a, int scale) {
  if (a == 0 || w <= 0 || h <= 0) {
    return; // nothing over the menu's own background
  }
  NSRect rc = aiu_rect(x, y, w, h, scale);
  [aiu_colour(r, g, b, a) setFill];
  if (radius > 0) {
    CGFloat rad = radius / (CGFloat)(scale < 1 ? 1 : scale);
    [[NSBezierPath bezierPathWithRoundedRect:rc xRadius:rad yRadius:rad] fill];
  } else {
    [NSBezierPath fillRect:rc];
  }
}

void aiu_text(int x, int y, int w, int h, const char *s, int font, uint8_t r, uint8_t g, uint8_t b, uint8_t a, bool right, bool ellipsis, int scale) {
  @autoreleasepool {
    NSFont *f = aiu_font(font);
    NSMutableParagraphStyle *ps = [[NSMutableParagraphStyle alloc] init];
    ps.alignment = right ? NSTextAlignmentRight : NSTextAlignmentLeft;
    ps.lineBreakMode = ellipsis ? NSLineBreakByTruncatingTail : NSLineBreakByClipping;
    NSDictionary *attrs = @{NSFontAttributeName : f, NSForegroundColorAttributeName : aiu_colour(r, g, b, a), NSParagraphStyleAttributeName : ps};
    NSRect box = aiu_rect(x, y, w, h, scale);
    CGFloat line = aiu_line(f);
    // vertically centred in its box; the rectangle one line high (a line
    // that does not fit its rectangle would not be drawn at all)
    NSRect rc = NSMakeRect(box.origin.x, box.origin.y + (box.size.height - line) / 2, box.size.width, ceil(line) + 1);
    [aiu_string(s) drawWithRect:rc options:NSStringDrawingUsesLineFragmentOrigin | NSStringDrawingTruncatesLastVisibleLine attributes:attrs context:nil];
  }
}

void aiu_pixels(int x, int y, int w, int h, const uint8_t *bgra, uint8_t opacity, int scale) {
  if (w <= 0 || h <= 0 || bgra == NULL) {
    return;
  }
  @autoreleasepool {
    CFDataRef data = CFDataCreate(NULL, bgra, (CFIndex)w * h * 4);
    CGDataProviderRef provider = CGDataProviderCreateWithCFData(data);
    CGColorSpaceRef srgb = CGColorSpaceCreateWithName(kCGColorSpaceSRGB);
    // premultiplied blue, green, red, alpha in memory: alpha first in a
    // little-endian 32-bit word
    CGImageRef img = CGImageCreate(w, h, 8, 32, w * 4, srgb, kCGBitmapByteOrder32Little | kCGImageAlphaPremultipliedFirst,
                                   provider, NULL, false, kCGRenderingIntentDefault);
    CGColorSpaceRelease(srgb);
    CGDataProviderRelease(provider);
    CFRelease(data);
    if (img == NULL) {
      return;
    }
    NSRect rc = aiu_rect(x, y, w, h, scale);
    NSImage *image = [[NSImage alloc] initWithCGImage:img size:rc.size];
    CGImageRelease(img);
    [image drawInRect:rc fromRect:NSZeroRect operation:NSCompositingOperationSourceOver fraction:opacity / 255.0 respectFlipped:YES
                hints:@{NSImageHintInterpolation : @(NSImageInterpolationNone)}];
  }
}

// c in sRGB as four bytes (resolved for the current drawing appearance)
static void aiu_bytes(NSColor *c, uint8_t *out) {
  NSColor *s = [c colorUsingColorSpace:[NSColorSpace sRGBColorSpace]];
  CGFloat r = 0, g = 0, b = 0, a = 1;
  if (s != nil) {
    [s getRed:&r green:&g blue:&b alpha:&a];
  }
  out[0] = (uint8_t)lround(fmin(fmax(r, 0), 1) * 255);
  out[1] = (uint8_t)lround(fmin(fmax(g, 0), 1) * 255);
  out[2] = (uint8_t)lround(fmin(fmax(b, 0), 1) * 255);
  out[3] = (uint8_t)lround(fmin(fmax(a, 0), 1) * 255);
}

@interface AIUPanelView : NSView
@property(nonatomic) int itemId;
@property(nonatomic) int renderScale; // 0: the window's
@end

@implementation AIUPanelView

- (BOOL)isFlipped {
  return YES;
}

- (int)aiuScale {
  CGFloat s = self.renderScale > 0 ? self.renderScale : (self.window ? self.window.backingScaleFactor : NSScreen.mainScreen.backingScaleFactor);
  int n = (int)lround(s);
  return n < 1 ? 1 : (n > 3 ? 3 : n);
}

- (void)drawRect:(NSRect)dirty {
  NSMenuItem *mi = [self enclosingMenuItem];
  BOOL hl = mi != nil && mi.isEnabled && mi.isHighlighted;
  NSAppearance *ap = self.effectiveAppearance;
  BOOL dark = [[ap bestMatchFromAppearancesWithNames:@[ NSAppearanceNameAqua, NSAppearanceNameDarkAqua ]] isEqualToString:NSAppearanceNameDarkAqua];
  uint8_t colours[16] = {0};
  uint8_t *c = colours; // a block cannot capture the array itself
  [ap performAsCurrentDrawingAppearance:^{
    aiu_bytes([NSColor labelColor], c);
    aiu_bytes([NSColor secondaryLabelColor], c + 4);
    aiu_bytes([NSColor selectedContentBackgroundColor], c + 8);
    aiu_bytes([NSColor selectedMenuItemTextColor], c + 12);
  }];
  int s = [self aiuScale];
  NSSize sz = self.bounds.size;
  systray_panel_view_draw(self.itemId, (int)ceil(sz.width * s), (int)ceil(sz.height * s), s, colours, dark, hl);
}

// the item's action, as menuHandler: sends it: the menu closes first
- (void)aiuChoose:(NSMenuItem *)mi {
  aiuActed = YES;
  [[mi menu] cancelTracking];
  systray_menu_item_selected(self.itemId);
}

// a click on an enabled item (a row) is its action; a header ignores it
- (void)mouseUp:(NSEvent *)event {
  NSMenuItem *mi = [self enclosingMenuItem];
  if (mi == nil || !mi.isEnabled) {
    return;
  }
  [self aiuChoose:mi];
}

// The menu gives the highlighted view the keys it does not handle itself
// once the view can take them (its arrows and Escape stay the menu's):
// Return or keypad Enter on an enabled, highlighted item is its action.
- (BOOL)acceptsFirstResponder {
  return YES;
}

- (void)keyDown:(NSEvent *)event {
  NSMenuItem *mi = [self enclosingMenuItem];
  if ((event.keyCode == 36 || event.keyCode == 76) && mi != nil && mi.isEnabled && mi.isHighlighted) {
    [self aiuChoose:mi];
    return;
  }
  [super keyDown:event];
}

- (void)viewDidChangeEffectiveAppearance {
  [super viewDidChangeEffectiveAppearance];
  [self setNeedsDisplay:YES];
}

// VoiceOver: a menu item that says its texts
- (BOOL)isAccessibilityElement {
  return YES;
}

- (NSAccessibilityRole)accessibilityRole {
  return NSAccessibilityMenuItemRole;
}

- (NSString *)accessibilityLabel {
  char *s = systray_panel_view_label(self.itemId);
  NSString *out = aiu_string(s);
  free(s);
  return out;
}

- (BOOL)isAccessibilityEnabled {
  NSMenuItem *mi = [self enclosingMenuItem];
  return mi != nil && mi.isEnabled;
}

@end

// the view's size from the Go side (unchanged when it has none)
static void aiu_size(AIUPanelView *v) {
  int w = 0, h = 0;
  if (systray_panel_view_size(v.itemId, &w, &h) && w > 0 && h > 0) {
    [v setFrameSize:NSMakeSize(w, h)];
  }
  [v setNeedsDisplay:YES];
}

// Called on the main thread by systray_darwin.m's set_menu_item_panel:.
void aiu_panel_attach(NSMenuItem *mi, int menuId, bool on) {
  if (!on) {
    mi.view = nil;
    return;
  }
  AIUPanelView *v = [mi.view isKindOfClass:[AIUPanelView class]] ? (AIUPanelView *)mi.view : nil;
  if (v != nil && aiuMenuOpen) {
    return; // the open menu keeps what it opened with
  }
  if (v == nil) {
    v = [[AIUPanelView alloc] initWithFrame:NSMakeRect(0, 0, 1, 1)];
    v.itemId = menuId;
    // as wide as the menu when the menu is wider (NSMenuItem.view)
    v.autoresizingMask = NSViewWidthSizable;
    mi.view = v;
  }
  aiu_size(v);
}

// The menu delegate's calls (systray_darwin.m).
void aiu_panel_menu_will_open(NSMenu *menu) {
  aiuMenuOpen = YES;
  aiuActed = NO;
  aiuLastHighlighted = nil;
  systray_panel_menu_opening();
  for (NSMenuItem *mi in menu.itemArray) {
    if ([mi.view isKindOfClass:[AIUPanelView class]]) {
      aiu_size((AIUPanelView *)mi.view);
    }
  }
}

void aiu_panel_will_highlight(NSMenuItem *item) {
  if (item != nil) {
    aiuLastHighlighted = item;
  }
}

void aiu_panel_note_action(void) {
  aiuActed = YES;
}

// Return or Enter on a highlighted view item closes the menu without its
// action (stage 0, Z8): when no action came and the event that closed the
// menu is that key (its key-down or key-up), the item last highlighted,
// if it is an enabled view item, gets its action now. Escape and every
// other way of closing do nothing.
void aiu_panel_menu_did_close(NSMenu *menu) {
  NSMenuItem *last = aiuLastHighlighted;
  BOOL acted = aiuActed;
  aiuMenuOpen = NO;
  aiuActed = NO;
  aiuLastHighlighted = nil;
  NSEvent *e = [NSApp currentEvent];
  BOOL enter = e != nil && (e.type == NSEventTypeKeyDown || e.type == NSEventTypeKeyUp) && (e.keyCode == 36 || e.keyCode == 76);
  if (!acted && enter && last != nil && last.isEnabled && [last.view isKindOfClass:[AIUPanelView class]] && last.menu == menu) {
    NSInteger i = [menu indexOfItem:last];
    if (i >= 0) {
      [menu performActionForItemAtIndex:i];
    }
  }
  systray_panel_menu_closed();
}

void *aiu_render_png(int menuId, int width, bool dark, int scale, int *length) {
  *length = 0;
  @autoreleasepool {
    [NSApplication sharedApplication];
    int w = 0, h = 0;
    if (!systray_panel_view_size(menuId, &w, &h) || w <= 0 || h <= 0) {
      return NULL;
    }
    if (width > w) {
      w = width;
    }
    if (scale < 1) {
      scale = 1;
    }
    AIUPanelView *v = [[AIUPanelView alloc] initWithFrame:NSMakeRect(0, 0, w, h)];
    v.itemId = menuId;
    v.renderScale = scale;
    NSAppearance *ap = [NSAppearance appearanceNamed:dark ? NSAppearanceNameDarkAqua : NSAppearanceNameAqua];
    v.appearance = ap;
    NSBitmapImageRep * (^bitmap)(void) = ^NSBitmapImageRep *(void) {
      NSBitmapImageRep *r = [[NSBitmapImageRep alloc] initWithBitmapDataPlanes:NULL
                                                                    pixelsWide:w * scale
                                                                    pixelsHigh:h * scale
                                                                 bitsPerSample:8
                                                               samplesPerPixel:4
                                                                      hasAlpha:YES
                                                                      isPlanar:NO
                                                                colorSpaceName:NSCalibratedRGBColorSpace
                                                                   bytesPerRow:0
                                                                  bitsPerPixel:0];
      r = [r bitmapImageRepByRetaggingWithColorSpace:[NSColorSpace sRGBColorSpace]];
      r.size = NSMakeSize(w, h);
      return r;
    };
    // the view as the menu draws it, then over the window background
    // colour (cacheDisplayInRect: may or may not clear what is under the
    // view; the background goes under it in a picture of its own)
    NSBitmapImageRep *drawn = bitmap();
    NSBitmapImageRep *out = bitmap();
    NSGraphicsContext *ctx = out ? [NSGraphicsContext graphicsContextWithBitmapImageRep:out] : nil;
    if (drawn == nil || ctx == nil) {
      return NULL;
    }
    [ap performAsCurrentDrawingAppearance:^{
      [v cacheDisplayInRect:v.bounds toBitmapImageRep:drawn];
      [NSGraphicsContext saveGraphicsState];
      [NSGraphicsContext setCurrentContext:ctx];
      [[NSColor windowBackgroundColor] setFill];
      [NSBezierPath fillRect:NSMakeRect(0, 0, w, h)];
      [drawn drawInRect:NSMakeRect(0, 0, w, h) fromRect:NSZeroRect operation:NSCompositingOperationSourceOver fraction:1 respectFlipped:NO hints:nil];
      [ctx flushGraphics];
      [NSGraphicsContext restoreGraphicsState];
    }];
    NSData *png = [out representationUsingType:NSBitmapImageFileTypePNG properties:@{}];
    if (png == nil || png.length == 0) {
      return NULL;
    }
    void *buf = malloc(png.length);
    if (buf == NULL) {
      return NULL;
    }
    memcpy(buf, png.bytes, png.length);
    *length = (int)png.length;
    return buf;
  }
}
