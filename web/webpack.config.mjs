/*
 * Copyright (c) 2022-2026 Super Durable, Inc.
 * Licensed under the Apache License, Version 2.0.
 * SPDX-License-Identifier: Apache-2.0
 */

import path from "node:path";
import { fileURLToPath } from "node:url";

const directory = path.dirname(fileURLToPath(import.meta.url));

export default {
  entry: "./src/index.tsx",
  output: {
    filename: "bundle.js",
    path: path.resolve(directory, "../internal/webui/assets"),
    clean: { keep: /index\.html$/ },
  },
  module: {
    rules: [
      { test: /\.tsx?$/, use: "ts-loader", exclude: /node_modules/ },
      {
        test: /\.css$/,
        type: "asset/resource",
        generator: { filename: "styles.css" },
      },
    ],
  },
  resolve: { extensions: [".tsx", ".ts", ".js"] },
};
