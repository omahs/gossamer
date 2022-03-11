# Copyright 2021 ChainSafe Systems (ON)
# SPDX-License-Identifier: LGPL-3.0-only

# syntax=docker/dockerfile:1

FROM node:16.0.0
ENV NODE_ENV=production

WORKDIR /transaction

COPY ["package.json", "package-lock.json", "./"]

RUN npm install --production

#RUN npm install -g mocha

COPY . .

#RUN cd ./tests/transaction

ENTRYPOINT npm test

EXPOSE 8546/tcp