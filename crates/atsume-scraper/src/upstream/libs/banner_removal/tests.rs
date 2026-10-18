use std::path::{Path, PathBuf};
use std::sync::Arc;

use image::{ColorType, DynamicImage, GrayImage, ImageFormat, Luma, RgbaImage};
use tempfile::TempDir;

use super::banner_templates::BannerTemplates;
use super::one_bit_image::{Area, OneBitImage};
use crate::testing::{Checkout, module_source};
use crate::upstream::libs::image_codec::{encode_jpeg, encode_png};

const WHITE: Luma<u8> = Luma([255]);

/// Draws something shaped like the banner: a white gap at the top, then dark
/// marks across the rest.
fn banner(width: u32, height: u32) -> GrayImage {
    GrayImage::from_fn(width, height, |x, y| {
        let inside = (6..height - 2).contains(&y) && (4..width - 4).contains(&x);
        if inside && (x / 7 + y / 5) % 3 != 0 {
            Luma([20])
        } else {
            WHITE
        }
    })
}

/// Draws a page of artwork, with `banner` stamped across its bottom if given.
fn page(width: u32, height: u32, banner: Option<&GrayImage>) -> GrayImage {
    let mut page = GrayImage::from_fn(width, height, |x, y| {
        if (x + y) % 11 < 4 { Luma([40]) } else { WHITE }
    });
    if let Some(banner) = banner {
        let left = (width - banner.width()) / 2;
        let top = height - banner.height();
        image::imageops::replace(&mut page, banner, left.into(), top.into());
    }
    page
}

fn write_jpeg(path: &Path, image: &GrayImage) {
    let encoded = encode_jpeg(&DynamicImage::ImageLuma8(image.clone())).unwrap();
    std::fs::write(path, encoded).unwrap();
}

/// Writes the banner as a template into `dir`, beside a file that is not an
/// image.
fn write_templates(dir: &Path, banner: &GrayImage) {
    std::fs::create_dir_all(dir).unwrap();
    let png = encode_png(&DynamicImage::ImageLuma8(banner.clone())).unwrap();
    std::fs::write(dir.join("banner.png"), png).unwrap();
    std::fs::write(dir.join("notes.txt"), "not an image").unwrap();
}

/// Loads a template set holding a 728x60 banner, and returns it with the
/// banner.
fn templates() -> (BannerTemplates, GrayImage) {
    let dir = TempDir::new().unwrap();
    let banner = banner(728, 60);
    write_templates(dir.path(), &banner);
    let templates = BannerTemplates::read_dir(&dir.path().canonicalize().unwrap()).unwrap();
    (templates, banner)
}

/// Writes `image` as a JPEG page in a new directory and returns both.
fn saved_page(image: &GrayImage) -> (TempDir, PathBuf) {
    let dir = TempDir::new().unwrap();
    let path = dir.path().join("page.jpg");
    write_jpeg(&path, image);
    (dir, path)
}

fn open_image(path: &Path) -> (ImageFormat, DynamicImage) {
    let bytes = std::fs::read(path).unwrap();
    let format = image::guess_format(&bytes).unwrap();
    (format, image::load_from_memory(&bytes).unwrap())
}

#[test]
fn a_page_with_the_banner_loses_exactly_the_banner() {
    let (templates, banner) = templates();
    assert_eq!(templates.len(), 1, "the text file is not a template");
    let (_dir, path) = saved_page(&page(728, 500, Some(&banner)));

    assert!(templates.remove_banner_from_file(&path, false).unwrap());
    let (format, image) = open_image(&path);
    assert_eq!(format, ImageFormat::Jpeg);
    assert_eq!((image.width(), image.height()), (728, 440));
    assert_eq!(
        image.color(),
        ColorType::L8,
        "a greyscale page stays greyscale"
    );
}

#[test]
fn a_page_without_the_banner_is_left_untouched() {
    let (templates, _) = templates();
    let (_dir, path) = saved_page(&page(728, 500, None));
    let before = std::fs::read(&path).unwrap();

    assert!(!templates.remove_banner_from_file(&path, false).unwrap());
    assert_eq!(std::fs::read(&path).unwrap(), before);
}

#[test]
fn saving_as_png_replaces_the_original() {
    let (templates, banner) = templates();
    let (dir, path) = saved_page(&page(728, 500, Some(&banner)));

    assert!(templates.remove_banner_from_file(&path, true).unwrap());
    assert!(!path.exists(), "the original should not survive");
    let (format, image) = open_image(&dir.path().join("page.png"));
    assert_eq!(format, ImageFormat::Png);
    assert_eq!(image.height(), 440);
}

#[test]
fn a_strip_with_ink_in_its_top_rows_is_artwork() {
    let (templates, _) = templates();
    let mut inked = banner(728, 60);
    for x in 0..728 {
        inked.put_pixel(x, 1, Luma([0]));
    }
    let (_dir, path) = saved_page(&page(728, 500, Some(&inked)));

    assert!(!templates.remove_banner_from_file(&path, false).unwrap());
}

#[test]
fn a_page_that_is_all_banner_is_left_untouched() {
    let (templates, banner) = templates();
    let (_dir, path) = saved_page(&banner);
    let before = std::fs::read(&path).unwrap();

    assert!(!templates.remove_banner_from_file(&path, false).unwrap());
    assert_eq!(std::fs::read(&path).unwrap(), before);
}

#[test]
fn removal_reports_a_page_that_cannot_be_read() {
    let (templates, _) = templates();
    let dir = TempDir::new().unwrap();
    let missing = dir.path().join("missing.jpg");
    assert!(templates.remove_banner_from_file(&missing, false).is_err());
    let not_an_image = dir.path().join("page.jpg");
    std::fs::write(&not_an_image, "not an image").unwrap();
    let error = templates
        .remove_banner_from_file(&not_an_image, false)
        .unwrap_err();
    assert!(error.to_string().contains("decode"), "{error}");
}

#[test]
fn a_directory_is_read_once() {
    let dir = TempDir::new().unwrap();
    write_templates(dir.path(), &banner(728, 60));
    let dir = dir.path().canonicalize().unwrap();
    let first = BannerTemplates::cached_or_read(&dir).unwrap();
    std::fs::remove_file(dir.join("banner.png")).unwrap();
    let second = BannerTemplates::cached_or_read(&dir).unwrap();
    assert!(Arc::ptr_eq(&first, &second));
    assert_eq!(second.len(), 1);
}

#[test]
fn identical_images_score_highest_and_different_sizes_score_zero() {
    let image = DynamicImage::ImageLuma8(banner(40, 20)).into_rgba8();
    let one_bit = OneBitImage::threshold_area(&image, Area::whole(&image));
    assert!((one_bit.peak_signal_to_noise_ratio(&one_bit) - 1e6).abs() < f64::EPSILON);
    let smaller = OneBitImage::threshold_area(
        &image,
        Area {
            width: 30,
            ..Area::whole(&image)
        },
    );
    assert!(one_bit.peak_signal_to_noise_ratio(&smaller).abs() < f64::EPSILON);
}

#[test]
fn an_area_larger_than_the_image_surrounds_it_with_white() {
    let black = RgbaImage::from_pixel(2, 1, image::Rgba([0, 0, 0, 255]));
    let area = Area {
        width: 6,
        height: 2,
        ..Area::whole(&black)
    };
    let padded = OneBitImage::threshold_area(&black, area);
    // The image is centered in the top row, and the row below it is white.
    let expected = RgbaImage::from_fn(6, 2, |x, y| {
        if y == 0 && (2..4).contains(&x) {
            image::Rgba([0, 0, 0, 255])
        } else {
            image::Rgba([255; 4])
        }
    });
    let expected = OneBitImage::threshold_area(&expected, Area::whole(&expected));
    assert!(padded.peak_signal_to_noise_ratio(&expected) >= 1e6);
    assert!(!padded.top_rows_are_white(1));
}

/// A module that loads the templates in `lua/templates` and removes the
/// banner from each saved page, plus a hook that tries to edit `other`.
fn watermark_module(other: &Path) -> String {
    let init = "local m = NewWebsiteModule()
        m.Name = 'Watermarked'
        m.OnAfterImageSaved = 'AfterImageSaved'
        local wm = require 'fmd.mangafoxwatermark'
        COUNT = wm.LoadTemplate(require('fmd.env').LuaDirectory .. 'templates\\\\')";
    let rest = format!(
        "local wm = require 'fmd.mangafoxwatermark'
        function AfterImageSaved()
            REMOVED = wm.RemoveWatermark(FILENAME, false)
            if EDIT_OTHER then wm.RemoveWatermark({other:?}, false) end
            return true
        end"
    );
    module_source(init, &rest)
}

fn watermark_checkout(other: &Path, banner: &GrayImage) -> Checkout {
    let checkout = Checkout::new(&[("modules/Test.lua", &watermark_module(other))]);
    write_templates(&checkout.lua_dir().join("templates"), banner);
    checkout
}

#[test]
fn a_module_removes_the_banner_from_the_page_being_saved() {
    let banner = banner(728, 60);
    let (_dir, path) = saved_page(&page(728, 500, Some(&banner)));
    let checkout = watermark_checkout(&path, &banner);
    let mut session = checkout.open("modules/Test.lua", None, None).unwrap();
    assert_eq!(session.eval("tostring(COUNT)"), "1");

    session.after_image_saved(&path).unwrap();
    assert_eq!(session.eval("tostring(REMOVED)"), "true");
    assert_eq!(open_image(&path).1.height(), 440);
}

#[test]
fn a_module_may_edit_no_file_but_the_page_being_saved() {
    let banner = banner(728, 60);
    let (dir, page_path) = saved_page(&page(728, 500, Some(&banner)));
    let other = dir.path().join("other.jpg");
    write_jpeg(&other, &page(728, 500, Some(&banner)));
    let before = std::fs::read(&other).unwrap();
    let checkout = watermark_checkout(&other, &banner);
    let mut session = checkout.open("modules/Test.lua", None, None).unwrap();

    session.exec("EDIT_OTHER = true");
    let error = session.after_image_saved(&page_path).unwrap_err();
    assert!(
        error.to_string().contains("is not the page being saved"),
        "{error}"
    );
    assert_eq!(std::fs::read(&other).unwrap(), before);

    // Outside the hook, no file is the page being saved.
    let outside = format!(
        "tostring(select(2, pcall(require('fmd.mangafoxwatermark').RemoveWatermark, {:?}, false)))",
        page_path.display().to_string()
    );
    assert!(
        session
            .eval(&outside)
            .contains("is not the page being saved")
    );
}

#[test]
fn templates_load_only_from_inside_the_checkout() {
    let outside = TempDir::new().unwrap();
    write_templates(outside.path(), &banner(728, 60));
    let checkout = Checkout::new(&[("modules/Test.lua", &module_source("NewWebsiteModule()", ""))]);
    let session = checkout.open("modules/Test.lua", None, None).unwrap();
    let load = |dir: &str| {
        session.eval(&format!(
            "tostring(require('fmd.mangafoxwatermark').LoadTemplate({dir:?}))"
        ))
    };
    assert_eq!(load(&outside.path().display().to_string()), "0");
    assert_eq!(load("lua/nonexistent"), "0");
    assert_eq!(load("../.."), "0");
}
