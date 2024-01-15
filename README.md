# [go.astrophena.name](https://go.astrophena.name)

This is code that generates pages for my [Go vanity import](https://pkg.go.dev/cmd/go#hdr-Remote_import_paths)
domain, hosted on [GitHub Pages](https://pages.github.com).

This code is bloody [omnishambles](https://en.wikipedia.org/wiki/Omnishambles).
Don't use it.

## Building

If you insist:

```sh
$ git clone https://github.com/astrophena/vanity
$ cd vanity
$ export GITHUB_TOKEN="$(gh auth token)"
$ ./build.go
```

You'll find HTML pages in `build` directory.

## License

[ISC](LICENSE.md) © [Ilya Mateyko](https://github.com/astrophena)
