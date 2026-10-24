// Default naming Vars program. Keep this file valid JavaScript and side-effect free.
// This file only seeds the first version; edit the program in Settings afterwards.
// Every value is exposed as a string.
// The evaluator provides input.release_title, input.files ({name, path, size}),
// input.release.title, input.torrent.size and input.torrent.file_count.
// This preset explicitly chooses an empty optional tag when no episode is captured.
vars.anime = {ep: extract(input.release_title, /(?:^|\s)(\d{1,4})(?:\D|$)/) ?? ""};
