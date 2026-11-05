# ---- ขั้น build ----
FROM golang:1.24 AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
# timetzdata = ฝังข้อมูลเขตเวลาในไฟล์โปรแกรม (distroless ไม่มี tzdata) ทำให้ TZ=Asia/Bangkok ใช้ได้
RUN CGO_ENABLED=0 go build -trimpath -tags timetzdata \
      -ldflags "-s -w -X main.version=${VERSION}" \
      -o /out/zkscan ./cmd/zkscan \
 && mkdir /data-seed

# ---- ขั้นรันจริง (ไม่ใช้ root) ----
FROM gcr.io/distroless/static-debian12:nonroot
# 65532 = ผู้ใช้ nonroot ของ distroless; สร้าง /data ให้เขียนได้ก่อนประกาศ VOLUME
# (volume ใหม่จะรับสิทธิ์จากโฟลเดอร์นี้)
COPY --from=builder --chown=65532:65532 /data-seed /data
COPY --from=builder /out/zkscan /usr/local/bin/zkscan
ENV ZKSCAN_DATA=/data \
    ZKSCAN_LISTEN=:6565
VOLUME /data
EXPOSE 6565
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/zkscan"]
CMD ["serve"]
