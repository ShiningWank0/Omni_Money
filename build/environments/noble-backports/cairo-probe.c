#include <cairo-ft.h>
#include <stdint.h>
#include <stdio.h>
#include <string.h>

int main(int argc, char **argv)
{
    FT_Library library;
    FT_Face face;
    if (argc != 2 || FT_Init_FreeType(&library) ||
        FT_New_Face(library, argv[1], 0, &face))
        return 1;

    cairo_font_face_t *font = cairo_ft_font_face_create_for_ft_face(face, 0);
    cairo_surface_t *surface = cairo_image_surface_create(CAIRO_FORMAT_ARGB32, 96, 64);
    cairo_t *cr = cairo_create(surface);
    cairo_set_source_rgb(cr, 1, 0, 0);
    cairo_rectangle(cr, 2.5, 2.5, 40, 30);
    cairo_fill(cr);
    cairo_set_font_face(cr, font);
    cairo_set_font_size(cr, 14);
    cairo_move_to(cr, 4, 52);
    cairo_show_text(cr, "Omni");
    cairo_surface_flush(surface);
    int ok = cairo_status(cr) == CAIRO_STATUS_SUCCESS &&
             cairo_surface_status(surface) == CAIRO_STATUS_SUCCESS;
    uint32_t pixel = 0;
    if (ok) {
        memcpy(&pixel, cairo_image_surface_get_data(surface) +
               10 * cairo_image_surface_get_stride(surface) + 10 * sizeof(pixel), sizeof(pixel));
        ok = pixel == UINT32_C(0xffff0000);
    }
    cairo_destroy(cr);
    cairo_surface_destroy(surface);
    cairo_font_face_destroy(font);
    cairo_debug_reset_static_data();
    ok = ok && FT_Get_Char_Index(face, 'A') != 0;
    FT_Done_Face(face);
    FT_Done_FreeType(library);
    return ok ? 0 : 1;
}
