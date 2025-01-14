FROM ubuntu

# Compile and install fresh ffmpeg from sources:
# See: https://trac.ffmpeg.org/wiki/CompilationGuide/Ubuntu
RUN apt-get update -qq && apt-get -y install \
  autoconf \
  automake \
  build-essential \
  cmake \
  git-core \
  libass-dev \
  libfreetype6-dev \
  libsdl2-dev \
  libtool \
  libva-dev \
  libvdpau-dev \
  libvorbis-dev \
  libxcb1-dev \
  libxcb-shm0-dev \
  libxcb-xfixes0-dev \
  pkg-config \
  texinfo \
  wget \
  zlib1g-dev \
  nasm \
  yasm \
  libx265-dev \
  libnuma-dev \
  libvpx-dev \
  libmp3lame-dev \
  libopus-dev \
  libx264-dev \
  libfdk-aac-dev \
  libsvtav1-dev libsvtav1enc-dev libsvtav1dec-dev

RUN mkdir -p ~/ffmpeg_sources ~/bin && cd ~/ffmpeg_sources && \
  wget -O ffmpeg-7.1.tar.bz2 https://ffmpeg.org/releases/ffmpeg-7.1.tar.bz2 && \
  tar xjvf ffmpeg-7.1.tar.bz2 && \
  cd ffmpeg-7.1 && \
  PATH="$HOME/bin:$PATH" PKG_CONFIG_PATH="$HOME/ffmpeg_build/lib/pkgconfig" ./configure \
  --prefix="$HOME/ffmpeg_build" \
  --pkg-config-flags="--static" \
  --extra-cflags="-I$HOME/ffmpeg_build/include" \
  --extra-ldflags="-L$HOME/ffmpeg_build/lib" \
  --extra-libs="-lpthread -lm" \
  --bindir="$HOME/bin" \
  --enable-libfdk-aac \
  --enable-gpl \
  --enable-libass \
  --enable-libfreetype \
  --enable-libmp3lame \
  --enable-libopus \
  --enable-libvorbis \
  --enable-libvpx \
  --enable-libx264 \
  --enable-libx265 \
  --enable-libsvtav1 \
  --enable-static \
  --enable-nonfree && \
  PATH="$HOME/bin:$PATH" make -j24 && \
  make install -j24

RUN mv ~/bin/ffmpeg /usr/local/bin && mv ~/bin/ffprobe /usr/local/bin && mv ~/bin/ffplay /usr/local/bin
