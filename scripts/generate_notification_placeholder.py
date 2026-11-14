from pathlib import Path

from PIL import Image, ImageDraw

WIDTH, HEIGHT = 600, 800
image = Image.new("RGB", (WIDTH, HEIGHT))
pixels = image.load()
for y in range(HEIGHT):
    for x in range(WIDTH):
        t = (x / WIDTH + y / HEIGHT) / 2
        pixels[x, y] = (
            int(30 + 35 * t),
            int(38 + 28 * t),
            int(67 + 52 * t),
        )

draw = ImageDraw.Draw(image)
# A neutral clapperboard mark: the placeholder intentionally carries no title,
# provider branding, or generated metadata.
draw.rounded_rectangle((170, 330, 430, 545), radius=24, outline=(225, 232, 245), width=18)
draw.polygon([(160, 300), (410, 245), (430, 320), (180, 375)], outline=(225, 232, 245), width=18)
for x in (205, 275, 345):
    draw.line((x, 290, x + 35, 345), fill=(225, 232, 245), width=14)
draw.ellipse((280, 410, 320, 450), fill=(225, 232, 245))

output = Path(__file__).parents[1] / "internal/plugintarget/application/assets/notification_placeholder.jpg"
output.parent.mkdir(parents=True, exist_ok=True)
image.save(output, "JPEG", quality=88, optimize=True)
print(output)
