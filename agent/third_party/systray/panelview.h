// Not upstream code: this file was added to the copy of fyne.io/systray
// v1.12.2 in agent/third_party/systray of the AI Usage client (see
// PATCHED.md). It is under the AI Usage client's MIT licence (LICENSE at
// the repository's root), not the Apache License 2.0 of the copied code.
//
// Menu items whose view the AI Usage usage panel draws (macOS): the Go
// side (panelview_darwin.go) decides what each view shows and where; the
// Objective-C side (panelview_darwin.m) measures text and carries the
// drawing steps out.

#include <stdbool.h>
#include <stdint.h>

// Exported by panelview_darwin.go; called on the main thread.
extern bool systray_panel_view_size(int menu_id, int *width, int *height);
extern bool systray_panel_view_draw(int menu_id, int width, int height, int scale, uint8_t *colours, bool dark, bool highlighted);
extern char *systray_panel_view_label(int menu_id);
extern void systray_panel_menu_opening(void);
extern void systray_panel_menu_closed(void);

// In systray_darwin.m: gives the item a view (or takes it away), on the
// main thread.
void set_menu_item_panel(int menuId, bool on);

// In panelview_darwin.m: measuring and drawing for the Go side, in pixels
// at scale pixels per point; drawing goes to the current graphics context
// (the view's, while it draws).
int aiu_text_width(int font, const char *s, int scale);
int aiu_line_height(int font, int scale);
void aiu_fill(int x, int y, int w, int h, int radius, uint8_t r, uint8_t g, uint8_t b, uint8_t a, int scale);
void aiu_text(int x, int y, int w, int h, const char *s, int font, uint8_t r, uint8_t g, uint8_t b, uint8_t a, bool right, bool ellipsis, int scale);
void aiu_pixels(int x, int y, int w, int h, const uint8_t *bgra, uint8_t opacity, int scale);
// The item's view drawn alone into a PNG (malloc'd; NULL on failure), at
// least width points wide, in the light or dark appearance, over the
// window background colour; for tests, on the main thread.
void *aiu_render_png(int menuId, int width, bool dark, int scale, int *length);
