/*
 * Copyright (c) 2022-2026 Super Durable, Inc.
 * Licensed under the Apache License, Version 2.0.
 * SPDX-License-Identifier: Apache-2.0
 */

import path from "node:path";
import { fileURLToPath } from "node:url";
import CopyPlugin from "copy-webpack-plugin";
import HtmlWebpackPlugin from "html-webpack-plugin";
import MiniCssExtractPlugin from "mini-css-extract-plugin";

const directory = path.dirname(fileURLToPath(import.meta.url));

export default {
  entry: "./src/index.tsx",
  output: {
    filename: "[name].[contenthash:16].js",
    chunkFilename: "[name].[contenthash:16].js",
    path: path.resolve(directory, "dist"),
    clean: true,
  },
  module: {
    rules: [
      { test: /\.tsx?$/, use: "ts-loader", exclude: /node_modules/ },
      {
        test: /\.css$/,
        use: [MiniCssExtractPlugin.loader, "css-loader"],
      },
    ],
  },
  plugins: [
    new HtmlWebpackPlugin({
      template: path.resolve(directory, "public/index.html"),
      scriptLoading: "defer",
    }),
    new MiniCssExtractPlugin({
      filename: "[name].[contenthash:16].css",
      chunkFilename: "[name].[contenthash:16].css",
    }),
    new CopyPlugin({
      patterns: [
        {
          from: path.resolve(directory, "public/config.json"),
          to: "config.json",
        },
        {
          from: path.resolve(directory, "../LICENSE"),
          to: "LICENSE",
          toType: "file",
        },
      ],
    }),
  ],
  resolve: { extensions: [".tsx", ".ts", ".js"] },
};
