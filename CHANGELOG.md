# Changelog

## [1.2.1](https://github.com/coin-book/coin-book-api/compare/v1.2.0...v1.2.1) (2026-10-20)


### Performance

* **api:** link tables carry their owner, transfer and report queries use indexes ([9505c5a](https://github.com/coin-book/coin-book-api/commit/9505c5af0c9f11eb610e4b6a59cf859f4f00f364))

## [1.2.0](https://github.com/coin-book/coin-book-api/compare/v1.1.0...v1.2.0) (2026-10-20)


### Features

* **api:** net worth, mortgages with an offset reserve, Fingood and Portu imports ([c08a40c](https://github.com/coin-book/coin-book-api/commit/c08a40c9e2235b37e823afc5519bf613a6a5cec6))

## [1.1.0](https://github.com/coin-book/coin-book-api/compare/v1.0.0...v1.1.0) (2026-10-18)


### Features

* **api:** column mapper for any bank's CSV, kept as templates ([c25c506](https://github.com/coin-book/coin-book-api/commit/c25c5066f0f2f44c31d81361d3162666ab47d572))
* **api:** import the ABO (GPC) statement every Czech bank exports ([a7f237f](https://github.com/coin-book/coin-book-api/commit/a7f237f0a75bf03cfe61e0c66b77966970a9bae2))
* **api:** import the Air Bank account history CSV ([c7f431c](https://github.com/coin-book/coin-book-api/commit/c7f431cf1ce01d0ea86f785f7b1d0ce39e8a618c))
* **api:** net worth with weekly trend, english category names and triage of a picked transaction ([b90ea3b](https://github.com/coin-book/coin-book-api/commit/b90ea3be1df5540ea5dc70e7c19c6e30e4c488d8))
* **api:** positions per account, the symbol's market profile and the holding summary ([fceb1bd](https://github.com/coin-book/coin-book-api/commit/fceb1bdf790ae24429361ce21c0641050a1598d2))
* **api:** recognise rows already imported from another bank format, refresh rows a newer parser reads better ([a2283c6](https://github.com/coin-book/coin-book-api/commit/a2283c652b16ab47398dd1f2668a2f2066c7de0d))


### Bug Fixes

* **api:** a mapped file's account is named after its template, its made-up reference is not shown ([7909887](https://github.com/coin-book/coin-book-api/commit/7909887fb8ac6d50472f6bfc8839d69d060d811c))
* **api:** ABO import reads the currency column only when it holds one ISO code ([7d8eb4e](https://github.com/coin-book/coin-book-api/commit/7d8eb4e10fea3cbdf788fcf2d292d63493cb00f3))

## 1.0.0 (2026-10-18)


### Features

* **api:** account mapping, bank register, learned classifier, user categories and own transfers ([4de66a9](https://github.com/coin-book/coin-book-api/commit/4de66a9428a3180b1ff1d5559734795176477f89))
* **api:** import Revolut holdings and price them from Yahoo and ČNB ([71dbe5d](https://github.com/coin-book/coin-book-api/commit/71dbe5da847d44c3ea4ed44ee50d6f6339ccd313))
* **api:** initial import, categorization, transfers, budgets and agent access ([1b8a6a2](https://github.com/coin-book/coin-book-api/commit/1b8a6a247b16a4682759550fc2a8accfad49d608))
* **api:** money flow and category-by-month reports, twelve-month trend ([2441ab7](https://github.com/coin-book/coin-book-api/commit/2441ab72b27676f06bc2d9b0ce2c0d7b4d660d45))
* **api:** search ignores diacritics and matches amounts ([f5e97f8](https://github.com/coin-book/coin-book-api/commit/f5e97f8938b5a93d4c30df86eb9dada16d031cb3))
* **api:** several users with row-level security, open registration and Google sign-in ([d3df174](https://github.com/coin-book/coin-book-api/commit/d3df17462749ea1595bf4ccbf6929017db612335))
